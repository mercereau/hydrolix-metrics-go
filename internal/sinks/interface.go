package sinks

import (
	"time"
)

type Tags map[string]string

// MetricSink is the tiny surface your clients depend on.
type MetricSink interface {
	Start() // support for buffering and timers
	Stop()
	Gauge(name, unit string, value float64, tags Tags) // gauges (set)
	Inc(name, unit string, value float64, tags Tags)   // counter
	Rate(name, unit string, value float64, tags Tags)  // rate
	Timing(name string, d time.Duration, tags Tags)    // durations
	WithTags(tags Tags) MetricSink                     // scope base tags
	WithTimestamp(timestamp int64) MetricSink
}

// ---------- Helpers ----------

func StartTimer(s MetricSink, name string, tags Tags) func() {
	start := time.Now()
	return func() { s.Timing(name, time.Since(start), tags) }
}

func MergeTags(a, b Tags) Tags {
	if len(a) == 0 {
		// return copy of b to avoid external mutation
		out := make(Tags, len(b))
		for k, v := range b {
			out[k] = v
		}
		return out
	}
	out := make(Tags, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// ---------- No-op sink (safe default) ----------

type Nop struct{ base Tags }

func NewNop(base Tags) Nop                      { return Nop{base: base} }
func (Nop) Start()                              {}
func (Nop) Stop()                               {}
func (Nop) Gauge(string, string, float64, Tags) {}
func (Nop) Inc(string, string, float64, Tags)   {}
func (Nop) Rate(string, string, float64, Tags)  {}
func (Nop) Timing(string, time.Duration, Tags)  {}
func (n Nop) WithTags(t Tags) MetricSink        { return Nop{base: MergeTags(n.base, t)} }
func (n Nop) WithTimestamp(int64) MetricSink    { return Nop{base: n.base} }

type MetricSinks []MetricSink

func (ms MetricSinks) Gauge(name string, unit string, value float64, tags Tags) {
	for _, s := range ms {
		s.Gauge(name, unit, value, tags)
	}
}

func (ms MetricSinks) Inc(name string, unit string, value float64, tags Tags) {
	for _, s := range ms {
		s.Inc(name, unit, value, tags)
	}
}

func (ms MetricSinks) Rate(name string, unit string, value float64, tags Tags) {
	for _, s := range ms {
		s.Rate(name, unit, value, tags)
	}
}

func (ms MetricSinks) Start() {
	for _, s := range ms {
		s.Start()
	}
}

func (ms MetricSinks) Stop() {
	for _, s := range ms {
		s.Stop()
	}
}

func (ms MetricSinks) Timing(name string, d time.Duration, tags Tags) {
	for _, s := range ms {
		s.Timing(name, d, tags)
	}
}

func (ms MetricSinks) WithTags(tags Tags) MetricSink {
	result := make(MetricSinks, len(ms))
	for i, s := range ms {
		result[i] = s.WithTags(tags)
	}
	return result
}

func (ms MetricSinks) WithTimestamp(ts int64) MetricSink {
	result := make(MetricSinks, len(ms))
	for i, s := range ms {
		result[i] = s.WithTimestamp(ts)
	}
	return result
}
