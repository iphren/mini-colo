// Command mini-colo collects power and network usage, serves a live
// dashboard and writes monthly usage reports.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const usageText = `usage: mini-colo <command>

commands:
  collect   take one sample and print it as JSON
  serve     collect samples continuously and serve the dashboard
  report    write a monthly usage report (see: mini-colo report -h)
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mini-colo:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}

	if err := loadDotEnv(".env"); err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	// ctx is cancelled on Ctrl-C or SIGTERM (what systemd sends on stop), so
	// everything that takes it gets a chance to shut down cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch args[0] {
	case "collect":
		return cmdCollect(ctx, cfg)
	case "serve":
		return cmdServe(ctx, cfg)
	case "report":
		return cmdReport(ctx, cfg, args[1:])
	default:
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
		return nil
	}
}

// collectTimeout bounds one sample, so a hung router or meter cannot stall
// the collector.
const collectTimeout = 30 * time.Second

func cmdCollect(ctx context.Context, cfg Config) error {
	ctx, cancel := context.WithTimeout(ctx, collectTimeout)
	defer cancel()

	sample, err := collect(ctx, cfg)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(sample)
}
