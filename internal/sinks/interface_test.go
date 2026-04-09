package sinks

import (
	"testing"
	"time"
)

func TestMergeTags(t *testing.T) {
	a := Tags{"x": "1"}
	b := Tags{"y": "2"}
	m := MergeTags(a, b)
	if m["x"] != "1" || m["y"] != "2" {
		t.Fatalf("MergeTags incorrect: %#v", m)
	}
	// empty a returns copy of b
	m2 := MergeTags(Tags{}, b)
	if m2["y"] != "2" {
		t.Fatalf("MergeTags empty a incorrect: %#v", m2)
	}
}

type _dummySink struct{ called *bool }

func (d _dummySink) Start()                              {}
func (d _dummySink) Stop()                               {}
func (d _dummySink) Gauge(string, string, float64, Tags) {}
func (d _dummySink) Inc(string, string, float64, Tags)   {}
func (d _dummySink) Rate(string, string, float64, Tags)  {}
func (d _dummySink) Timing(name string, dur time.Duration, tags Tags) {
	if d.called != nil {
		*d.called = true
	}
}
func (d _dummySink) WithTags(t Tags) MetricSink     { return d }
func (d _dummySink) WithTimestamp(int64) MetricSink { return d }

func TestStartTimer(t *testing.T) {
	called := false
	d := _dummySink{called: &called}
	stop := StartTimer(d, "t", Tags{})
	time.Sleep(1 * time.Millisecond)
	stop()
	if !called {
		t.Fatalf("expected Timing to be called")
	}
}
