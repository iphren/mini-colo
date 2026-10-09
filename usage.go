package main

import (
	"math"
	"time"
)

// Usage is what was consumed over a period, derived from the cumulative
// counters in consecutive samples.
type Usage struct {
	Start time.Time

	EnergyWh float64
	RxKiB    int64
	TxKiB    int64
	// Seconds is how much time the totals above cover. It is less than the
	// period's length when samples are missing.
	Seconds float64

	// Temperature is a reading, not a counter, so it is summarised rather
	// than summed. Samples counts the readings that went into it.
	Samples int
	TempMin float64
	TempMax float64
	tempSum float64
}

// TempAvg is the mean temperature, or NaN if the period has no samples.
func (u Usage) TempAvg() float64 {
	if u.Samples == 0 {
		return math.NaN()
	}
	return u.tempSum / float64(u.Samples)
}

// PowerW is the average power draw, or NaN if the period has no usage data.
func (u Usage) PowerW() float64 {
	return u.perSecond(u.EnergyWh * 3600)
}

// RxRate and TxRate are average transfer rates in KiB per second.
func (u Usage) RxRate() float64 { return u.perSecond(float64(u.RxKiB)) }
func (u Usage) TxRate() float64 { return u.perSecond(float64(u.TxKiB)) }

func (u Usage) perSecond(total float64) float64 {
	if u.Seconds == 0 {
		return math.NaN()
	}
	return total / u.Seconds
}

// counterDelta is how much a cumulative counter grew between two readings.
// A counter that went backwards was reset (the device rebooted or its
// database was recreated), in which case everything it shows now has
// accumulated since the reset.
func counterDelta[T int64 | float64](prev, cur T) T {
	if cur < prev {
		return cur
	}
	return cur - prev
}

// bucketize splits [from, to) into consecutive periods and totals the usage
// in each. next returns the end of the period that starts at its argument,
// which lets callers choose fixed-length or calendar periods.
//
// samples must be in time order. Samples before from are not counted, but
// the last of them is the baseline for the first sample in range, as
// returned by Store.Range. The usage between two samples is attributed to
// the period the later one falls in.
func bucketize(samples []Sample, from, to time.Time, next func(time.Time) time.Time) []Usage {
	var buckets []Usage
	for start := from; start.Before(to); start = next(start) {
		buckets = append(buckets, Usage{Start: start})
	}

	i := 0 // index of the bucket the current sample falls in
	var prev *Sample
	for _, cur := range samples {
		if cur.Time.Before(from) {
			prev = &cur
			continue
		}
		if !cur.Time.Before(to) {
			break
		}
		for i+1 < len(buckets) && !cur.Time.Before(buckets[i+1].Start) {
			i++
		}
		b := &buckets[i]

		if b.Samples == 0 || cur.TemperatureC < b.TempMin {
			b.TempMin = cur.TemperatureC
		}
		if b.Samples == 0 || cur.TemperatureC > b.TempMax {
			b.TempMax = cur.TemperatureC
		}
		b.tempSum += cur.TemperatureC
		b.Samples++

		if prev != nil {
			b.EnergyWh += counterDelta(prev.EnergyWh, cur.EnergyWh)
			b.RxKiB += counterDelta(prev.NetworkRxKiB, cur.NetworkRxKiB)
			b.TxKiB += counterDelta(prev.NetworkTxKiB, cur.NetworkTxKiB)
			b.Seconds += cur.Time.Sub(prev.Time).Seconds()
		}
		prev = &cur
	}
	return buckets
}

// totalUsage is the usage over [from, to) as a single period.
func totalUsage(samples []Sample, from, to time.Time) Usage {
	return bucketize(samples, from, to, func(time.Time) time.Time { return to })[0]
}

// every returns a next function for fixed-length periods.
func every(d time.Duration) func(time.Time) time.Time {
	return func(t time.Time) time.Time { return t.Add(d) }
}

// nextDay is a next function for calendar days. Unlike every(24*time.Hour)
// it stays on midnight across daylight-saving changes.
func nextDay(t time.Time) time.Time {
	return t.AddDate(0, 0, 1)
}
