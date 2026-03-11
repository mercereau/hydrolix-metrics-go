package collector

import "testing"

// Minimal compile-time test that MetricSink interface is satisfiable by a local type.
type dummySink struct{}

func (d *dummySink) Start()                {}
func (d *dummySink) Stop()                 {}
func (d *dummySink) Metrics() []MetricSink { return nil }

func TestDummyImplementsMetricSink(t *testing.T) {
	var _ MetricSink = &dummySink{}
}
