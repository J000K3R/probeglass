package main

import (
	"context"
	"flag"
	"os"
	"time"

	"github.com/j000k3r/probeglass/internal/config"
	"github.com/j000k3r/probeglass/internal/doctor"
)

// runDoctor reports what is wrong with this installation, and changes nothing.
//
// It is built for the container. The image is distroless: no shell, no curl, no
// tools to fall back on, so `docker exec <container> probeglass doctor` is the
// one command a stuck user has, and everything it needs has to be in the binary.
//
// Flags mirror serve and seed, so the three agree about where the configuration
// and the database are. An empty value means "fall through to the environment,
// then the file, then the default", which is what makes the bare form work
// inside the container, where PROBEGLASS_DATA_DIR is already set.
//
// It exits 0 when nothing failed — warnings are not failures, and a fresh
// install warns — and 1 otherwise. That introduces no new exit code, so the
// documented table stays true.
func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to the YAML configuration file")
	dataDir := fs.String("data", "", "directory holding the database")
	listen := fs.String("listen", "", "address the daemon is listening on")
	timeout := fs.Duration("timeout", 10*time.Second, "how long the whole run may take")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Resolve, not Load. Load fails hard on a configuration file it cannot use,
	// which is right for serve and useless here: the reader most needs the paths
	// precisely when the file is unreadable, and a nil config would leave this
	// command unable to name the data directory it was asked about. The error is
	// not dropped, it is handed to the report and shown as a finding.
	cfg, cfgErr := config.Resolve(*configPath, config.Overrides{
		Listen:  *listen,
		DataDir: *dataDir,
	})

	report := doctor.Run(context.Background(), doctor.Options{
		Config:     cfg,
		ConfigErr:  cfgErr,
		HealthAddr: config.DialAddr(cfg.Listen),
		Timeout:    *timeout,
	})
	report.Render(os.Stdout)

	if report.Failed() {
		// The report is already on stdout. The sentinel keeps main from printing
		// a second, redundant line under it.
		return errFindings
	}
	return nil
}
