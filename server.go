package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"
)

// webFS holds the dashboard's static files, compiled into the binary so the
// program deploys as a single file.
//
//go:embed web
var webFS embed.FS

type server struct {
	cfg   Config
	store *Store
}

// cmdServe runs the collector and the dashboard until ctx is cancelled.
func cmdServe(ctx context.Context, cfg Config) error {
	store, err := openStore(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()

	// The collector runs in its own goroutine next to the HTTP server.
	// The WaitGroup makes sure it has finished before the deferred Close
	// above takes the database away from it.
	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Go(func() { collectLoop(ctx, cfg, store) })

	s := &server{cfg: cfg, store: store}
	httpServer := &http.Server{Addr: cfg.ListenAddr, Handler: s.routes()}

	// ListenAndServe blocks, so shutting down has to be triggered from
	// another goroutine once ctx is cancelled.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdownCtx)
	}()

	slog.Info("serving dashboard", "addr", cfg.ListenAddr, "db", cfg.DBPath, "interval", cfg.Interval)
	if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// collectLoop stores a sample immediately and then once per interval. A
// failed sample is logged and skipped; the next tick tries again.
func collectLoop(ctx context.Context, cfg Config, store *Store) {
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	for {
		collectCtx, cancel := context.WithTimeout(ctx, collectTimeout)
		sample, err := collect(collectCtx, cfg)
		if err == nil {
			err = store.Insert(collectCtx, sample)
		}
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Error("collecting sample", "err", err)
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

func (s *server) routes() http.Handler {
	static, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err) // the embedded directory is fixed at compile time
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/dashboard", s.handleDashboard)
	mux.HandleFunc("GET /report", s.handleReport)
	mux.Handle("GET /", http.FileServerFS(static))
	return mux
}

// chartRange is one choice in the dashboard's time-range filter: how far
// back to look and how wide each plotted point is.
type chartRange struct {
	span   time.Duration
	bucket time.Duration
}

var chartRanges = map[string]chartRange{
	"6h":  {6 * time.Hour, 5 * time.Minute},
	"24h": {24 * time.Hour, 15 * time.Minute},
	"7d":  {7 * 24 * time.Hour, time.Hour},
	"30d": {30 * 24 * time.Hour, 6 * time.Hour},
}

// The types below are the JSON the dashboard page fetches. Pointers are
// used where a value can be absent: nil encodes as null, whereas NaN cannot
// be encoded at all.

type dashboardJSON struct {
	Latest *latestJSON `json:"latest"`
	Month  totalsJSON  `json:"month"`
	Series seriesJSON  `json:"series"`
	// StaleAfter is how old the latest sample may be, in seconds, before
	// the page should warn that collection has stopped.
	StaleAfter float64 `json:"stale_after"`
}

type latestJSON struct {
	Time         time.Time `json:"timestamp"`
	TemperatureC float64   `json:"temperature_c"`
	PowerW       *float64  `json:"power_w"`
	// PowerSeconds is the length of the period PowerW is averaged over.
	PowerSeconds float64 `json:"power_seconds"`
}

type totalsJSON struct {
	Label    string   `json:"label"`
	EnergyWh float64  `json:"energy_wh"`
	Cost     *float64 `json:"cost"`
	Currency string   `json:"currency"`
	RxKiB    int64    `json:"rx_kib"`
	TxKiB    int64    `json:"tx_kib"`
}

type seriesJSON struct {
	Time         []int64    `json:"t"` // bucket start, Unix milliseconds
	PowerW       []*float64 `json:"power_w"`
	TemperatureC []*float64 `json:"temperature_c"`
	RxKiBPerSec  []*float64 `json:"rx_kib_s"`
	TxKiBPerSec  []*float64 `json:"tx_kib_s"`
}

// powerWindow is the shortest period "power now" is averaged over. The
// energy counter moves in small steps, so a single short interval would be
// jumpy. When samples are further apart than this, the average covers the
// time since the previous sample instead.
const powerWindow = 5 * time.Minute

func (s *server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("range")
	if name == "" {
		name = "24h"
	}
	cr, ok := chartRanges[name]
	if !ok {
		http.Error(w, "unknown range", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	now := time.Now()
	resp := dashboardJSON{StaleAfter: (3 * s.cfg.Interval).Seconds()}

	latest, ok, err := s.store.Latest(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	if ok {
		recent, err := s.store.Range(ctx, latest.Time.Add(-powerWindow), latest.Time.Add(time.Second))
		if err != nil {
			s.fail(w, err)
			return
		}
		u := totalUsage(recent, latest.Time.Add(-powerWindow), latest.Time.Add(time.Second))
		resp.Latest = &latestJSON{
			Time:         latest.Time,
			TemperatureC: latest.TemperatureC,
			PowerW:       nullable(u.PowerW()),
			PowerSeconds: u.Seconds,
		}
	}

	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	monthEnd := monthStart.AddDate(0, 1, 0)
	monthSamples, err := s.store.Range(ctx, monthStart, monthEnd)
	if err != nil {
		s.fail(w, err)
		return
	}
	resp.Month = s.totals(monthStart.Format("January 2006"), totalUsage(monthSamples, monthStart, monthEnd))

	// Round the end up to a bucket boundary so buckets keep the same
	// edges from one refresh to the next.
	// A bucket narrower than the collection interval would mostly be empty
	// and break the chart's line into dots, so widen it to match.
	bucket := max(cr.bucket, s.cfg.Interval)
	to := now.Truncate(bucket).Add(bucket)
	from := to.Add(-cr.span)
	samples, err := s.store.Range(ctx, from, to)
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, b := range bucketize(samples, from, to, every(bucket)) {
		resp.Series.Time = append(resp.Series.Time, b.Start.UnixMilli())
		resp.Series.PowerW = append(resp.Series.PowerW, nullable(b.PowerW()))
		resp.Series.TemperatureC = append(resp.Series.TemperatureC, nullable(b.TempAvg()))
		resp.Series.RxKiBPerSec = append(resp.Series.RxKiBPerSec, nullable(b.RxRate()))
		resp.Series.TxKiBPerSec = append(resp.Series.TxKiBPerSec, nullable(b.TxRate()))
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("writing response", "err", err)
	}
}

func (s *server) totals(label string, u Usage) totalsJSON {
	t := totalsJSON{
		Label:    label,
		EnergyWh: u.EnergyWh,
		Currency: s.cfg.Currency,
		RxKiB:    u.RxKiB,
		TxKiB:    u.TxKiB,
	}
	if s.cfg.PricePerKWh > 0 {
		cost := u.EnergyWh / 1000 * s.cfg.PricePerKWh
		t.Cost = &cost
	}
	return t
}

func (s *server) fail(w http.ResponseWriter, err error) {
	slog.Error("handling request", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// nullable turns NaN, which means "no data" in usage.go, into nil.
func nullable(v float64) *float64 {
	if math.IsNaN(v) {
		return nil
	}
	return &v
}
