// Package statsd provides a StatsD-backed MetricSink that sends metrics over
// UDP using the DogStatsD extended format. DogStatsD is backwards-compatible
// with plain StatsD -- daemons that don't understand tags simply ignore them.
package statsd

import (
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/gertd/go-pluralize"
	metrics "github.com/mercereau/hydrolix-metrics-go/internal/sinks"
)

var pl *pluralize.Client

func init() {
	pl = pluralize.NewClient()
}

type StatsdOpts struct {
	Namespace string
	Subsystem string
	Addr      string            // "host:port", default "localhost:8125"
	BaseTags  map[string]string // tags applied to every metric
}

type Sink struct {
	opts StatsdOpts
	base metrics.Tags
	mu   sync.RWMutex
	conn net.Conn
}

// statsdScoped is an immutable scoped view of a Sink with fixed base tags.
// WithTags returns a new statsdScoped rather than mutating the root Sink.
type statsdScoped struct {
	root *Sink
	base metrics.Tags
}

func NewSink(o StatsdOpts) *Sink {
	if o.Addr == "" {
		o.Addr = "localhost:8125"
	}
	return &Sink{
		opts: o,
		base: o.BaseTags,
	}
}

func (s *Sink) Start() {
	conn, err := net.Dial("udp", s.opts.Addr)
	if err != nil {
		slog.Error("Failed to connect to StatsD", "addr", s.opts.Addr, "error", err)
		return
	}
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()
	slog.Info("StatsD sink started", "addr", s.opts.Addr)
}

func (s *Sink) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
	}
	slog.Info("StatsD sink stopped")
}

func (s *Sink) send(line string) {
	s.mu.RLock()
	conn := s.conn
	s.mu.RUnlock()
	if conn == nil {
		return
	}
	if _, err := fmt.Fprint(conn, line); err != nil {
		slog.Debug("StatsD send failed", "error", err)
	}
}

func (s *Sink) buildName(name, unit string) string {
	unit = pl.Plural(unit)
	return fmt.Sprintf("%s.%s.%s.%s", s.opts.Namespace, s.opts.Subsystem, name, unit)
}

func (s *Sink) Gauge(name, unit string, value float64, tags metrics.Tags) {
	s.send(formatLine(s.buildName(name, unit), value, "g", metrics.MergeTags(s.base, tags)))
}

func (s *Sink) Inc(name, unit string, value float64, tags metrics.Tags) {
	s.send(formatLine(s.buildName(name, unit), value, "c", metrics.MergeTags(s.base, tags)))
}

func (s *Sink) Rate(name, unit string, value float64, tags metrics.Tags) {
	s.send(formatLine(s.buildName(name, unit), value, "c", metrics.MergeTags(s.base, tags)))
}

func (s *Sink) Timing(name string, d time.Duration, tags metrics.Tags) {
	s.send(formatLine(s.buildName(name, "second"), d.Seconds()*1000, "ms", metrics.MergeTags(s.base, tags)))
}

func (s *Sink) WithTags(t metrics.Tags) metrics.MetricSink {
	return &statsdScoped{root: s, base: metrics.MergeTags(s.base, t)}
}

// WithTimestamp returns a scoped view; StatsD does not support per-metric timestamps.
func (s *Sink) WithTimestamp(int64) metrics.MetricSink {
	return &statsdScoped{root: s, base: s.base}
}

// ---------- statsdScoped ----------

func (ss *statsdScoped) Start() {}
func (ss *statsdScoped) Stop()  {}

func (ss *statsdScoped) Gauge(name, unit string, value float64, tags metrics.Tags) {
	ss.root.send(formatLine(ss.root.buildName(name, unit), value, "g", metrics.MergeTags(ss.base, tags)))
}

func (ss *statsdScoped) Inc(name, unit string, value float64, tags metrics.Tags) {
	ss.root.send(formatLine(ss.root.buildName(name, unit), value, "c", metrics.MergeTags(ss.base, tags)))
}

func (ss *statsdScoped) Rate(name, unit string, value float64, tags metrics.Tags) {
	ss.root.send(formatLine(ss.root.buildName(name, unit), value, "c", metrics.MergeTags(ss.base, tags)))
}

func (ss *statsdScoped) Timing(name string, d time.Duration, tags metrics.Tags) {
	ss.root.send(formatLine(ss.root.buildName(name, "second"), d.Seconds()*1000, "ms", metrics.MergeTags(ss.base, tags)))
}

func (ss *statsdScoped) WithTags(t metrics.Tags) metrics.MetricSink {
	return &statsdScoped{root: ss.root, base: metrics.MergeTags(ss.base, t)}
}

func (ss *statsdScoped) WithTimestamp(int64) metrics.MetricSink {
	return &statsdScoped{root: ss.root, base: ss.base}
}

// ---------- DogStatsD wire format ----------

// formatLine formats a metric in DogStatsD format:
// metric.name:value|type|#tag1:val1,tag2:val2
func formatLine(name string, value float64, typ string, tags metrics.Tags) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s:%g|%s", name, value, typ)
	if len(tags) > 0 {
		b.WriteString("|#")
		first := true
		for k, v := range tags {
			if !first {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, "%s:%s", k, v)
			first = false
		}
	}
	return b.String()
}
