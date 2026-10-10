// Command weatherglass is the monitoring daemon: it probes, stores, alerts and
// serves the dashboard.
//
// Subcommands:
//
//	weatherglass                     start the daemon (same as "serve")
//	weatherglass serve [flags]       start the daemon
//	weatherglass version             print the version
//	weatherglass healthcheck         probe the local HTTP endpoint, for HEALTHCHECK
//	weatherglass doctor              report what is wrong with this installation
//	weatherglass seed --demo         fill the database with demo sensors and history
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	// Registers the embedded timezone database. Without it, a scratch or
	// distroless image cannot resolve "Europe/Berlin" and every timestamp falls
	// back to UTC.
	_ "time/tzdata"

	"github.com/j000k3r/weatherglass/internal/api"
	"github.com/j000k3r/weatherglass/internal/config"
	"github.com/j000k3r/weatherglass/internal/event"
	"github.com/j000k3r/weatherglass/internal/model"
	"github.com/j000k3r/weatherglass/internal/notify"
	"github.com/j000k3r/weatherglass/internal/scheduler"
	"github.com/j000k3r/weatherglass/internal/sensor"
	"github.com/j000k3r/weatherglass/internal/store"
	"github.com/j000k3r/weatherglass/internal/web"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		// doctor prints its own report, so the sentinel it returns on a failure
		// is not a message to repeat. Everything else is.
		if !errors.Is(err, errFindings) {
			fmt.Fprintf(os.Stderr, "weatherglass: %v\n", err)
		}
		os.Exit(1)
	}
}

// errFindings is returned when doctor found something bad. It carries no text
// because the report has already been written to standard output, and printing
// "doctor found failures" underneath it would only repeat the summary line.
var errFindings = errors.New("doctor found failures")

func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "version", "-version", "--version":
			fmt.Printf("weatherglass %s\n", model.BuildInfo())
			return nil
		case "healthcheck":
			return healthcheck(args[1:])
		case "doctor":
			return runDoctor(args[1:])
		case "seed":
			return seed(args[1:])
		case "serve":
			args = args[1:]
		case "help", "-h", "--help":
			usage()
			return nil
		default:
			if len(args[0]) > 0 && args[0][0] != '-' {
				usage()
				return fmt.Errorf("unknown command %q", args[0])
			}
		}
	}
	return serve(args)
}

func usage() {
	fmt.Print(`weatherglass — a small, self-hosted watchdog for your homelab

Usage:
  weatherglass [serve] [flags]     start the daemon
  weatherglass version             print the version and exit
  weatherglass healthcheck         query the local /healthz endpoint
  weatherglass doctor              report what is wrong with this installation
  weatherglass seed --demo         create demo sensors with synthetic history

Flags for serve:
  -config PATH     YAML configuration file (default $WEATHERGLASS_CONFIG, else
                   <data dir>/weatherglass.yaml when it exists)
  -listen ADDR     address to serve the dashboard on (default :8080)
  -data DIR        directory for the database (default /data)
  -log-level LVL   debug, info, warn or error (default info)
  -log-format FMT  text or json (default text)

Every flag has a WEATHERGLASS_* environment variable equivalent; the environment
wins over the file. See docs/CONFIGURATION.md.
`)
}

// ---------------------------------------------------------------------------
// serve
// ---------------------------------------------------------------------------

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to the YAML configuration file")
	listen := fs.String("listen", "", "address to listen on, for example :8080")
	dataDir := fs.String("data", "", "directory holding the database")
	logLevel := fs.String("log-level", "", "debug, info, warn or error")
	logFormat := fs.String("log-format", "", "text or json")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath, config.Overrides{
		Listen:    *listen,
		DataDir:   *dataDir,
		LogLevel:  *logLevel,
		LogFormat: *logFormat,
	})
	if err != nil {
		return err
	}

	logger := newLogger(cfg)
	slog.SetDefault(logger)

	logger.Info("starting weatherglass",
		"version", model.Version,
		"listen", cfg.Listen,
		"data_dir", cfg.DataDir,
		"config", cfg.Path,
		"database", cfg.DBPath)

	st, err := store.Open(cfg.DBPath, logger)
	if err != nil {
		return err
	}
	defer st.Close()
	st.SetRetention(cfg.Retention.Raw.D(), cfg.Retention.Hourly.D())

	if err := applyDeclarativeConfig(context.Background(), st, cfg, logger); err != nil {
		return err
	}

	broker := event.NewBroker()
	defer broker.Close()

	dispatcher := notify.NewDispatcher(st, logger, cfg.BaseURL)
	sched := scheduler.New(st, dispatcher, broker, logger)

	server, err := api.New(context.Background(), api.Options{
		Config:     cfg,
		Store:      st,
		Scheduler:  sched,
		Dispatcher: dispatcher,
		Broker:     broker,
		Logger:     logger,
		Assets:     web.Assets(),
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := sched.Start(ctx); err != nil {
		return fmt.Errorf("start scheduler: %w", err)
	}
	defer sched.Stop()

	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: the dashboard keeps a Server-Sent Events stream open,
		// and a write deadline would cut it off.
		IdleTimeout: 120 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	// Bind first, announce second. ListenAndServe binds internally, so the
	// obvious ordering logs "listening" and only then discovers that it is not:
	// the terminal reads "dashboard listening" followed by a bind error, which
	// looks like the daemon started and something unrelated broke afterwards.
	// The usual cause is a previous copy of this same program still holding the
	// port, and that is exactly the case where a misleading log costs the most
	// time. Binding up front also means the port is known to be ours before the
	// scheduler starts doing work.
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		// The error already names the address and the reason, so this only adds
		// the one thing it cannot know: what to do about it.
		return fmt.Errorf("cannot listen on %s — is another copy of Weatherglass already running? (%w)", cfg.Listen, err)
	}

	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	logger.Info("dashboard listening", "url", "http://"+config.DisplayAddr(ln.Addr().String()))

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	// Give in-flight requests a moment; the scheduler stops with the context.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

func newLogger(cfg *config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

// ---------------------------------------------------------------------------
// declarative configuration
// ---------------------------------------------------------------------------

// applyDeclarativeConfig upserts the sites, sensors and channels from the YAML
// file. They are marked managed, which makes them read-only in the dashboard: the
// file stays the source of truth, and a user cannot end up with two versions of
// the same sensor that disagree.
func applyDeclarativeConfig(ctx context.Context, st *store.Store, cfg *config.Config, log *slog.Logger) error {
	// Channels first: sensors refer to them by name.
	for _, spec := range cfg.Channels {
		ch := spec.ToChannel()
		store.NormaliseChannel(&ch)
		if err := notify.ValidateChannel(ch); err != nil {
			return fmt.Errorf("channel %q: %w", spec.Name, err)
		}
		if err := st.UpsertManagedChannel(ctx, &ch); err != nil {
			return fmt.Errorf("channel %q: %w", spec.Name, err)
		}
	}
	if len(cfg.Channels) > 0 {
		log.Info("applied channels from the configuration file", "count", len(cfg.Channels))
	}

	// Sites come next: a sensor below may name one.
	//
	// Both halves are needed. A site written out in full is upserted and stays
	// managed — the file owns it, coordinates included. A site that is merely
	// named by a sensor is only guaranteed to exist: the row is created if it is
	// missing and left completely alone if it is not, because somebody may have
	// given it coordinates in the dashboard, and a file that has no opinion about
	// where that site is must not have the effect of wiping it.
	for _, spec := range cfg.Sites {
		site := spec.ToSite()
		store.NormaliseSite(&site)
		if err := st.UpsertManagedSite(ctx, &site); err != nil {
			return fmt.Errorf("site %q: %w", spec.Name, err)
		}
	}
	for _, spec := range cfg.Sensors {
		if spec.Site == "" {
			continue
		}
		if err := st.EnsureSite(ctx, spec.Site); err != nil {
			return fmt.Errorf("sensor %q: site %q: %w", spec.Name, spec.Site, err)
		}
	}
	if len(cfg.Sites) > 0 {
		log.Info("applied sites from the configuration file", "count", len(cfg.Sites))
	}

	for _, spec := range cfg.Sensors {
		c := spec.ToSensor()
		store.NormaliseSensor(&c)
		if err := sensor.Validate(c); err != nil {
			return fmt.Errorf("sensor %q: %w", spec.Name, err)
		}
		if len(spec.Channels) > 0 {
			ids, missing, err := st.ChannelIDsByNames(ctx, spec.Channels)
			if err != nil {
				return fmt.Errorf("sensor %q: %w", spec.Name, err)
			}
			if len(missing) > 0 {
				return fmt.Errorf("sensor %q refers to unknown alert channels: %v", spec.Name, missing)
			}
			c.Channels = ids
		}
		if err := st.UpsertManagedSensor(ctx, &c); err != nil {
			return fmt.Errorf("sensor %q: %w", spec.Name, err)
		}
	}
	if len(cfg.Sensors) > 0 {
		log.Info("applied sensors from the configuration file", "count", len(cfg.Sensors))
	}
	return nil
}

// ---------------------------------------------------------------------------
// healthcheck
// ---------------------------------------------------------------------------

// healthcheck lets the container image ship a HEALTHCHECK without pulling curl
// into a distroless layer.
func healthcheck(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	listen := fs.String("listen", "", "address the daemon is listening on")
	timeout := fs.Duration("timeout", 5*time.Second, "how long to wait")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load("", config.Overrides{Listen: *listen})
	if err != nil {
		return err
	}
	addr := config.DialAddr(cfg.Listen)

	// The deadline lives on the context rather than on the client, so that the
	// whole request — dial, headers, body — is bounded by the one value the
	// caller passed and nothing can outlive it.
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		return fmt.Errorf("healthcheck failed: %w", err)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck failed: HTTP %d", resp.StatusCode)
	}
	fmt.Println("ok")
	return nil
}
