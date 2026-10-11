package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/j000k3r/weatherglass/internal/config"
	"github.com/j000k3r/weatherglass/internal/demo"
	"github.com/j000k3r/weatherglass/internal/privdrop"
	"github.com/j000k3r/weatherglass/internal/store"
)

// seed fills the database with demo data. It exists so the dashboard can be
// looked at without waiting a week for history to accumulate.
//
// The dataset and the generator live in internal/demo, because the button in
// Settings that puts the example data back writes the same ones. This file is
// only the shell around them: the flags, a store, and a sentence.
func seed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	demoFlag := fs.Bool("demo", false, "create demo sensors with synthetic history")
	days := fs.Int("days", 7, "how many days of history to synthesise")
	configPath := fs.String("config", "", "path to the YAML configuration file")
	dataDir := fs.String("data", "", "directory holding the database")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*demoFlag {
		return fmt.Errorf("only \"seed --demo\" is supported: refusing to guess what to insert")
	}
	if *days < 1 || *days > 365 {
		return fmt.Errorf("days must be between 1 and 365")
	}

	cfg, err := config.Load(*configPath, config.Overrides{DataDir: *dataDir})
	if err != nil {
		return err
	}
	logger := newLogger(cfg)

	// Same preflight as serve: seed writes the database, so it needs the directory
	// just as much, and it must not do it as root either.
	if _, err := privdrop.Apply(privdrop.Options{
		DataDir: cfg.DataDir,
		DBPath:  cfg.DBPath,
		Logger:  logger,
	}); err != nil {
		return err
	}

	st, err := store.Open(cfg.DBPath, logger)
	if err != nil {
		return err
	}
	defer st.Close()
	st.SetRetention(cfg.Retention.Raw.D(), cfg.Retention.Hourly.D())

	res, err := demo.Seed(context.Background(), st, *days)
	if err != nil {
		return err
	}

	fmt.Printf("seeded %d new sensor(s), %d historical probes over %d day(s)\n",
		res.Sensors, res.Samples, *days)
	if res.Sites > 0 {
		fmt.Printf("created %d site(s) for them, so the map has something to draw\n", res.Sites)
	}
	fmt.Printf("database: %s\n", cfg.DBPath)
	fmt.Println("start the daemon and open the dashboard to see them")
	return nil
}
