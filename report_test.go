package main

import (
	"testing"
	"time"
)

func TestPeriodBounds(t *testing.T) {
	date := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	tests := []struct {
		name       string
		month      time.Time
		startDay   int
		start, end time.Time
		period     string
	}{
		{"calendar month", date(2026, 9, 1), 1, date(2026, 9, 1), date(2026, 10, 1), "September 2026"},
		{"mid-month", date(2026, 9, 1), 15, date(2026, 9, 15), date(2026, 10, 15), "15 Sep – 14 Oct 2026"},
		{"across new year", date(2026, 12, 1), 15, date(2026, 12, 15), date(2027, 1, 15), "15 Dec 2026 – 14 Jan 2027"},
		{"into short month", date(2027, 1, 1), 31, date(2027, 1, 31), date(2027, 2, 28), "31 Jan – 27 Feb 2027"},
		{"from short month", date(2027, 2, 1), 31, date(2027, 2, 28), date(2027, 3, 31), "28 Feb – 30 Mar 2027"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := periodBounds(tt.month, tt.startDay)
			if !start.Equal(tt.start) || !end.Equal(tt.end) {
				t.Errorf("periodBounds = %v, %v, want %v, %v", start, end, tt.start, tt.end)
			}
			if got := (report{Month: tt.month, Start: start, End: end}).Period(); got != tt.period {
				t.Errorf("Period() = %q, want %q", got, tt.period)
			}
		})
	}
}

func TestCurrentPeriod(t *testing.T) {
	tests := []struct {
		now      time.Time
		startDay int
		want     time.Month
	}{
		{time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC), 1, time.October},
		{time.Date(2026, 10, 14, 23, 59, 0, 0, time.UTC), 15, time.September},
		{time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), 15, time.October},
		{time.Date(2027, 2, 28, 0, 0, 0, 0, time.UTC), 31, time.February},
		{time.Date(2027, 2, 27, 0, 0, 0, 0, time.UTC), 31, time.January},
	}
	for _, tt := range tests {
		if got := currentPeriod(tt.now, tt.startDay).Month(); got != tt.want {
			t.Errorf("currentPeriod(%v, %d) = %v, want %v", tt.now, tt.startDay, got, tt.want)
		}
	}
}
