package main

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

// at builds a sample a number of minutes after t0.
func at(minutes int, energyWh, tempC float64, rx, tx int64) Sample {
	return Sample{
		Time:         t0.Add(time.Duration(minutes) * time.Minute),
		EnergyWh:     energyWh,
		TemperatureC: tempC,
		NetworkRxKiB: rx,
		NetworkTxKiB: tx,
	}
}

func TestCounterDelta(t *testing.T) {
	tests := []struct {
		name      string
		prev, cur float64
		want      float64
	}{
		{"growth", 10, 15, 5},
		{"unchanged", 10, 10, 0},
		{"reset", 10, 3, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := counterDelta(tt.prev, tt.cur); got != tt.want {
				t.Errorf("counterDelta(%v, %v) = %v, want %v", tt.prev, tt.cur, got, tt.want)
			}
		})
	}
}

func TestTotalUsage(t *testing.T) {
	samples := []Sample{
		at(-1, 100, 30, 1000, 100), // baseline, before the period
		at(0, 101, 40, 1010, 101),
		at(1, 103, 42, 1030, 103),
		at(2, 1, 44, 5, 2), // every counter reset
	}
	u := totalUsage(samples, t0, t0.Add(time.Hour))

	if u.EnergyWh != 4 { // 1 + 2 + 1
		t.Errorf("EnergyWh = %v, want 4", u.EnergyWh)
	}
	if u.RxKiB != 35 || u.TxKiB != 5 {
		t.Errorf("RxKiB, TxKiB = %v, %v, want 35, 5", u.RxKiB, u.TxKiB)
	}
	if u.Seconds != 180 {
		t.Errorf("Seconds = %v, want 180", u.Seconds)
	}
	if u.Samples != 3 || u.TempMin != 40 || u.TempMax != 44 || u.TempAvg() != 42 {
		t.Errorf("temperature = %d samples, min %v, max %v, avg %v; want 3, 40, 44, 42",
			u.Samples, u.TempMin, u.TempMax, u.TempAvg())
	}
	if got := u.PowerW(); got != 80 { // 4 Wh in 3 minutes
		t.Errorf("PowerW = %v, want 80", got)
	}
}

func TestBucketize(t *testing.T) {
	samples := []Sample{
		at(0, 100, 40, 0, 0),
		at(5, 110, 40, 0, 0),
		// Nothing in the second bucket: the collector was down.
		at(25, 150, 40, 0, 0),
	}
	buckets := bucketize(samples, t0, t0.Add(30*time.Minute), every(10*time.Minute))

	if len(buckets) != 3 {
		t.Fatalf("got %d buckets, want 3", len(buckets))
	}
	if buckets[0].EnergyWh != 10 {
		t.Errorf("bucket 0 EnergyWh = %v, want 10", buckets[0].EnergyWh)
	}
	if buckets[1].Samples != 0 || !math.IsNaN(buckets[1].PowerW()) {
		t.Errorf("bucket 1 should be empty, got %+v", buckets[1])
	}
	// The usage across the gap lands in the bucket that observed it, and
	// its rate is averaged over the whole gap rather than spiking.
	if got := buckets[2].PowerW(); got != 120 { // 40 Wh in 20 minutes
		t.Errorf("bucket 2 PowerW = %v, want 120", got)
	}
}

func TestNextDayAcrossDST(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Skip("no time zone database:", err)
	}
	// Clocks go back on 25 October 2026, making that day 25 hours long.
	from := time.Date(2026, 10, 24, 0, 0, 0, 0, london)
	buckets := bucketize(nil, from, from.AddDate(0, 0, 3), nextDay)

	if len(buckets) != 3 {
		t.Fatalf("got %d buckets, want 3", len(buckets))
	}
	for _, b := range buckets {
		if h := b.Start.Hour(); h != 0 {
			t.Errorf("bucket starts at %v, want midnight", b.Start)
		}
	}
}

func TestStoreRange(t *testing.T) {
	ctx := context.Background()
	store, err := openStore(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, ok, err := store.Latest(ctx); err != nil || ok {
		t.Fatalf("Latest on empty store = ok %v, err %v; want false, nil", ok, err)
	}

	for _, s := range []Sample{
		at(-2, 1, 40, 1, 1),
		at(-1, 2, 40, 2, 2),
		at(0, 3, 40, 3, 3),
		at(1, 4, 40, 4, 4),
		at(60, 5, 40, 5, 5),
	} {
		if err := store.Insert(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.Range(ctx, t0, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// One baseline sample before the range, then the two inside it.
	want := []Sample{at(-1, 2, 40, 2, 2), at(0, 3, 40, 3, 3), at(1, 4, 40, 4, 4)}
	if len(got) != len(want) {
		t.Fatalf("got %d samples, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sample %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	latest, ok, err := store.Latest(ctx)
	if err != nil || !ok || latest != at(60, 5, 40, 5, 5) {
		t.Errorf("Latest = %+v, ok %v, err %v", latest, ok, err)
	}
}
