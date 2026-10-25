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
	Title string
	// Month names the period, as the first of the month it starts in.
	// Start and End bound it; they differ from Month and the first of the
	// next month only when reports start on a day other than the 1st.
	Month      time.Time
	Start, End time.Time
	Generated  time.Time
	Total      Usage
	Days       []Usage
	// Coverage is the share of the month (so far) that the samples cover,
	// from 0 to 1. Anything under 1 means usage went unrecorded.
	Coverage float64

	PricePerKWh float64
	Currency    string

	// Prev and Next are neighbouring months to link to, in monthLayout,
	// or empty for no link. They are left empty in reports written to a
	// file, where there is nothing to link to.
	Prev, Next string
	// Favicon says whether to link to the dashboard's icon. Like the links
	// above, it only makes sense in a report the dashboard serves.
	Favicon bool

	// The stylesheet and chart code are inlined so a saved report is a
	// single self-contained file. Their types tell html/template that
	// they are trusted and must not be escaped.
	CSS     template.CSS
	ChartJS template.JS
}

// Period describes the span the report covers: just the month when it
// starts on the 1st, otherwise the first and last days.
func (r report) Period() string {
	if r.Start.Day() == 1 && r.End.Day() == 1 {
		return r.Month.Format("January 2006")
	}
	last := r.End.AddDate(0, 0, -1)
	if r.Start.Year() != last.Year() {
		return r.Start.Format("2 Jan 2006") + " – " + last.Format("2 Jan 2006")
	}
	return r.Start.Format("2 Jan") + " – " + last.Format("2 Jan 2006")
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

// periodBounds returns the reporting period that starts in month: from
// startDay of that month up to startDay of the next, in month's time zone.
// A startDay past the end of a short month falls on its last day.
func periodBounds(month time.Time, startDay int) (start, end time.Time) {
	first := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, month.Location())
	return dayOfMonth(first, startDay), dayOfMonth(first.AddDate(0, 1, 0), startDay)
}

// dayOfMonth is the given day of first's month, capped at the month's last
// day. time.Date would roll 31 February over into March instead.
func dayOfMonth(first time.Time, day int) time.Time {
	last := first.AddDate(0, 1, -1).Day()
	return time.Date(first.Year(), first.Month(), min(day, last), 0, 0, 0, 0, first.Location())
}

// currentPeriod returns the first of the month that the reporting period
// containing t starts in.
func currentPeriod(t time.Time, startDay int) time.Time {
	month := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	if start, _ := periodBounds(month, startDay); t.Before(start) {
		month = month.AddDate(0, -1, 0)
	}
	return month
}

// buildReport computes the report for the period starting in month (see
// periodBounds), in month's time zone.
func buildReport(ctx context.Context, store *Store, cfg Config, month time.Time) (report, error) {
	month = time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, month.Location())
	start, end := periodBounds(month, cfg.ReportStartDay)

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
		Title:       cfg.Title,
		Month:       month,
		Start:       start,
		End:         end,
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

// cmdReport writes one month's report to a file. It defaults to the last
// complete period, which is what a timer firing on REPORT_START_DAY wants.
func cmdReport(ctx context.Context, cfg Config, args []string) error {
	flags := flag.NewFlagSet("report", flag.ExitOnError)
	lastPeriod := currentPeriod(time.Now(), cfg.ReportStartDay).AddDate(0, -1, 0)
	monthFlag := flags.String("month", lastPeriod.Format(monthLayout), "month the period to report on starts in, as YYYY-MM")
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
// current period.
func (s *server) handleReport(w http.ResponseWriter, r *http.Request) {
	thisMonth := currentPeriod(time.Now(), s.cfg.ReportStartDay)

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

	rep.Favicon = s.cfg.FaviconPath != ""

	// Link back as far as there is data, and forward up to this period.
	earliest, ok, err := s.store.Earliest(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if ok && earliest.Time.Before(rep.Start) {
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
