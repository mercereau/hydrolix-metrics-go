package datadog

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metrics "github.com/mercereau/hydrolix-metrics-go/internal/sinks"
)

// countingSink records self-metric increments so tests can observe how many
// payloads reached the sender (sendToDatadog reports one send_failures per
// payload when MaxRetries is 0, without any HTTP traffic).
type countingSink struct {
	metrics.Nop
	counts *sync.Map
}

func (c countingSink) Inc(name, _ string, value float64, _ metrics.Tags) {
	v, _ := c.counts.LoadOrStore(name, new(atomic.Int64))
	v.(*atomic.Int64).Add(int64(value))
}
func (c countingSink) WithTags(metrics.Tags) metrics.MetricSink { return c }

func newTestSink(counts *sync.Map, concurrency uint16) *Sink {
	s := NewSink(DatadogOpts{
		Namespace: "t", Subsystem: "t",
		FlushInterval: time.Hour, // flush only on shutdown
		QueueSize:     8,
		BatchSize:     1 << 20, // never flush on batch size
		MaxRetries:    0,       // sendToDatadog consumes payloads without HTTP
		Concurrency:   concurrency,
		SelfSink:      countingSink{counts: counts},
	})
	s.Start()
	return s
}

func loadCount(counts *sync.Map, name string) int64 {
	if v, ok := counts.Load(name); ok {
		return v.(*atomic.Int64).Load()
	}
	return 0
}

// Metrics submitted concurrently with Stop must never panic or race: the
// closed check and the channel send have to be atomic with respect to Stop
// closing the channel. Run with -race.
func TestStopWhileSubmittingDoesNotPanic(t *testing.T) {
	var counts sync.Map
	s := newTestSink(&counts, 4)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					s.Gauge("m", "unit", 1, metrics.Tags{"k": "v"})
				}
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	s.Stop()
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// Metrics buffered in the collectors when Stop is called must still be
// delivered to the sender: Stop's contract is drain-then-exit, not drop.
func TestStopDeliversBufferedMetrics(t *testing.T) {
	var counts sync.Map
	s := newTestSink(&counts, 2)

	for i := 0; i < 10; i++ {
		s.Gauge("m", "unit", float64(i), nil)
	}
	// Let the collectors pick the metrics out of the channel into buffers.
	time.Sleep(100 * time.Millisecond)

	s.Stop()

	if dropped := loadCount(&counts, "hydrolix.sink.metrics_dropped"); dropped != 0 {
		t.Fatalf("metrics dropped on the way in: %d", dropped)
	}
	// With MaxRetries 0 every payload the sender consumes is counted as a
	// send failure; zero means the final flush never reached the sender.
	if sent := loadCount(&counts, "hydrolix.sink.send_failures"); sent == 0 {
		t.Error("buffered metrics were dropped at shutdown: no payload reached the sender")
	}
}
