package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is everything the program reads from the environment.
type Config struct {
	PowerStatusURL   string
	RouterHost       string
	NetworkInterface string

	// PricePerKWh and Currency are only used to show a cost next to energy
	// totals. A zero price hides the cost.
	PricePerKWh float64
	Currency    string

	ListenAddr string
	DBPath     string
	Interval   time.Duration
}

// loadDotEnv copies KEY=VALUE lines from path into the process environment.
// Variables that are already set win, so a real environment (for example a
// systemd unit) can override the file. A missing file is not an error.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s: expected KEY=VALUE, got %q", path, line)
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, set := os.LookupEnv(key); !set {
			os.Setenv(key, value)
		}
	}
	return scanner.Err()
}

func loadConfig() (Config, error) {
	cfg := Config{
		PowerStatusURL:   os.Getenv("POWER_STATUS_URL"),
		RouterHost:       os.Getenv("ROUTER_HOST"),
		NetworkInterface: os.Getenv("NETWORK_INTERFACE"),
		Currency:         os.Getenv("ENERGY_CURRENCY"),
		ListenAddr:       envOr("LISTEN_ADDR", ":8080"),
		DBPath:           envOr("DB_PATH", "mini-colo.db"),
	}

	// Collect every missing variable so the user can fix them in one go.
	var missing []string
	for name, value := range map[string]string{
		"POWER_STATUS_URL":  cfg.PowerStatusURL,
		"ROUTER_HOST":       cfg.RouterHost,
		"NETWORK_INTERFACE": cfg.NetworkInterface,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return cfg, fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}

	if s := os.Getenv("ENERGY_UNIT_PRICE_PER_KWH"); s != "" {
		price, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return cfg, fmt.Errorf("ENERGY_UNIT_PRICE_PER_KWH: %w", err)
		}
		cfg.PricePerKWh = price
	}

	interval, err := time.ParseDuration(envOr("COLLECT_INTERVAL", "1m"))
	if err != nil {
		return cfg, fmt.Errorf("COLLECT_INTERVAL: %w", err)
	}
	if interval <= 0 {
		return cfg, errors.New("COLLECT_INTERVAL must be positive")
	}
	cfg.Interval = interval

	return cfg, nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
