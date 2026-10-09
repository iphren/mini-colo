package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// Sample is one reading of the power meter and the router.
//
// EnergyWh, NetworkRxKiB and NetworkTxKiB are cumulative counters: they only
// grow, and usage over a period is the difference between two samples (see
// usage.go). TemperatureC is an instantaneous reading.
type Sample struct {
	Time         time.Time `json:"timestamp"`
	EnergyWh     float64   `json:"energy_wh"`
	TemperatureC float64   `json:"temperature_c"`
	NetworkRxKiB int64     `json:"network_rx_kib"`
	NetworkTxKiB int64     `json:"network_tx_kib"`
}

// collect takes one sample. It fails as a whole if either source fails, so
// the database never holds a half-filled row.
func collect(ctx context.Context, cfg Config) (Sample, error) {
	// Truncating to the second keeps timestamps tidy in JSON and makes them
	// round-trip exactly through the database, which stores Unix seconds.
	s := Sample{Time: time.Now().UTC().Truncate(time.Second)}

	power, err := fetchPower(ctx, cfg.PowerStatusURL)
	if err != nil {
		return s, fmt.Errorf("power: %w", err)
	}
	s.EnergyWh = power.AEnergy.Total
	s.TemperatureC = power.Temperature.TC

	network, err := fetchNetwork(ctx, cfg.RouterHost, cfg.NetworkInterface)
	if err != nil {
		return s, fmt.Errorf("network: %w", err)
	}
	s.NetworkRxKiB = network.Rx
	s.NetworkTxKiB = network.Tx

	return s, nil
}

// powerStatus is the part of the power meter's status response we use. The
// JSON decoder ignores fields that have no matching struct field.
type powerStatus struct {
	AEnergy struct {
		Total float64 `json:"total"`
	} `json:"aenergy"`
	Temperature struct {
		TC float64 `json:"tC"`
	} `json:"temperature"`
}

func fetchPower(ctx context.Context, url string) (powerStatus, error) {
	var status powerStatus

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return status, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return status, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return status, fmt.Errorf("unexpected status %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return status, fmt.Errorf("decoding response: %w", err)
	}
	return status, nil
}

// trafficTotal is vnstat's all-time counter for one interface, in KiB
// (vnstat 1.x; version 2 reports bytes).
type trafficTotal struct {
	Rx int64 `json:"rx"`
	Tx int64 `json:"tx"`
}

// fetchNetwork runs vnstat on the router over ssh. It shells out to the ssh
// binary rather than using a Go SSH library so that the host aliases, keys
// and known_hosts in ~/.ssh keep working unchanged.
func fetchNetwork(ctx context.Context, host, iface string) (trafficTotal, error) {
	var total trafficTotal

	remote := fmt.Sprintf("vnstat --json m -i '%s'", iface)
	// BatchMode makes ssh fail instead of waiting for a password prompt
	// that nobody will answer.
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", host, remote)
	out, err := cmd.Output()
	if err != nil {
		// Output captures stderr into ExitError when the command fails;
		// that is where ssh and vnstat explain themselves.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return total, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return total, err
	}

	var report struct {
		Interfaces []struct {
			Traffic struct {
				Total trafficTotal `json:"total"`
			} `json:"traffic"`
		} `json:"interfaces"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		return total, fmt.Errorf("decoding vnstat output: %w", err)
	}
	if len(report.Interfaces) == 0 {
		return total, fmt.Errorf("vnstat reported no data for interface %q", iface)
	}
	return report.Interfaces[0].Traffic.Total, nil
}
