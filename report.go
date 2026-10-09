package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"html/template"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

//go:embed templates/report.html
var reportHTML string

// monthLayout is how a month is written in URLs, flags and file names.
// Go layouts are written as this reference time: Mon Jan 2 15:04:05 2006.
const monthLayout = "2006-01"

// reportTemplate is parsed once at start-up; template.Must turns a syntax
// error in the template into a panic there rather than an error per request.
var reportTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"energy": formatEnergy,
	"bytes":  formatBytes,
	"temp":   formatTemp,
}).Parse(reportHTML))

// report is everything the template needs to render one month.
type report struct {
	Month     time.Time
	Generated time.Time
	Total     Usage
	Days      []Usage
	// Coverage is the share of the month (so far) that the samples cover,
	// from 0 to 1. Anything under 1 means usage went unrecorded.
	Coverage float64

	PricePerKWh float64
	Currency    string

	// Prev and Next are neighbouring months to link to, in monthLayout,
	// or empty for no link. They are left empty in reports written to a
	// file, where there is nothing to link to.
	Prev, Next string

	// The stylesheet and chart code are inlined so a saved report is a
	// single self-contained file. Their types tell html/template that
	// they are trusted and must not be escaped.
	CSS     template.CSS
	ChartJS template.JS
}

// CoveragePercent is Coverage formatted for display.
func (r report) CoveragePercent() string {
	return fmt.Sprintf("%.0f%%", math.Floor(r.Coverage*100))
}

// Cost is the price of an amount of energy, formatted for display.
func (r report) Cost(wh float64) string {
	return fmt.Sprintf("%s%.2f", r.Currency, wh/1000*r.PricePerKWh)
}

// reportDay is one day's usage as handed to the chart script. The template
// engine encodes it as JSON because it is used inside a <script> element.
type reportDay struct {
	// T is noon on the day, in Unix milliseconds. Noon rather than
	// midnight keeps the date right in a browser whose time zone differs
	// from the server's.
	T        int64    `json:"t"`
	EnergyWh *float64 `json:"energy_wh"`
	RxKiB    *float64 `json:"rx_kib"`
	TxKiB    *float64 `json:"tx_kib"`
}

func (r report) ChartDays() []reportDay {
	days := make([]reportDay, len(r.Days))
	for i, d := range r.Days {
		days[i].T = d.Start.Add(12 * time.Hour).UnixMilli()
		if d.Samples > 0 {
			rx, tx := float64(d.RxKiB), float64(d.TxKiB)
			days[i].EnergyWh, days[i].RxKiB, days[i].TxKiB = &d.EnergyWh, &rx, &tx
		}
	}
	return days
}

// buildReport computes the report for the calendar month containing month,
// in month's time zone.
func buildReport(ctx context.Context, store *Store, cfg Config, month time.Time) (report, error) {
	start := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, month.Location())
	end := start.AddDate(0, 1, 0)

	samples, err := store.Range(ctx, start, end)
	if err != nil {
		return report{}, err
	}
	css, err := webFS.ReadFile("web/style.css")
	if err != nil {
		return report{}, err
	}
	chartJS, err := webFS.ReadFile("web/chart.js")
	if err != nil {
		return report{}, err
	}

	r := report{
		Month:       start,
		Generated:   time.Now(),
		Total:       totalUsage(samples, start, end),
		Days:        bucketize(samples, start, end, nextDay),
		PricePerKWh: cfg.PricePerKWh,
		Currency:    cfg.Currency,
		CSS:         template.CSS(css),
		ChartJS:     template.JS(chartJS),
	}
	// For the month in progress, measure coverage against the time that
	// has actually passed.
	elapsedEnd := end
	if r.Generated.Before(end) {
		elapsedEnd = r.Generated
	}
	if elapsed := elapsedEnd.Sub(start).Seconds(); elapsed > 0 {
		r.Coverage = min(1, r.Total.Seconds/elapsed)
	}
	return r, nil
}

// cmdReport writes one month's report to a file. It defaults to last month,
// which is what a timer firing on the 1st wants.
func cmdReport(ctx context.Context, cfg Config, args []string) error {
	flags := flag.NewFlagSet("report", flag.ExitOnError)
	monthFlag := flags.String("month", time.Now().AddDate(0, -1, 0).Format(monthLayout), "month to report on, as YYYY-MM")
	outDir := flags.String("out", "reports", "directory to write the report into")
	flags.Parse(args)

	month, err := time.ParseInLocation(monthLayout, *monthFlag, time.Local)
	if err != nil {
		return fmt.Errorf("-month: %w", err)
	}

	store, err := openStore(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()

	r, err := buildReport(ctx, store, cfg, month)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(*outDir, month.Format(monthLayout)+".html")
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := reportTemplate.Execute(f, r); err != nil {
		return err
	}
	// Close can report a failed write, so check it rather than relying on
	// the deferred call, which ignores the error.
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

// handleReport serves the report for ?month=YYYY-MM, defaulting to the
// current month.
func (s *server) handleReport(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	thisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	month := thisMonth
	if q := r.URL.Query().Get("month"); q != "" {
		var err error
		month, err = time.ParseInLocation(monthLayout, q, time.Local)
		if err != nil {
			http.Error(w, "month must look like 2026-09", http.StatusBadRequest)
			return
		}
	}

	rep, err := buildReport(r.Context(), s.store, s.cfg, month)
	if err != nil {
		s.fail(w, err)
		return
	}

	// Link back as far as there is data, and forward up to this month.
	earliest, ok, err := s.store.Earliest(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if ok && earliest.Time.Before(rep.Month) {
		rep.Prev = rep.Month.AddDate(0, -1, 0).Format(monthLayout)
	}
	if rep.Month.Before(thisMonth) {
		rep.Next = rep.Month.AddDate(0, 1, 0).Format(monthLayout)
	}

	if err := reportTemplate.Execute(w, rep); err != nil {
		// The response is already partly written, so all that is left
		// to do is log.
		slog.Error("rendering report", "err", err)
	}
}

// ---- Formatting, shared with the template ---------------------------------

func formatEnergy(wh float64) string {
	if wh < 1000 {
		return fmt.Sprintf("%.1f Wh", wh)
	}
	return fmt.Sprintf("%.2f kWh", wh/1000)
}

func formatBytes(kib int64) string {
	value := float64(kib)
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d KiB", kib)
	}
	return fmt.Sprintf("%.2f %s", value, units[unit])
}

func formatTemp(c float64) string {
	if math.IsNaN(c) {
		return "–"
	}
	return fmt.Sprintf("%.1f °C", c)
}
