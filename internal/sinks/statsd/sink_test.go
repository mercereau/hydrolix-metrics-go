package statsd

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mercereau/hydrolix-metrics-go/internal/sinks"
)

func TestFormatLine_Gauge(t *testing.T) {
	got := formatLine("cdn.edge.bytes", 1234.5, "g", sinks.Tags{"env": "prod"})
	if !strings.HasPrefix(got, "cdn.edge.bytes:1234.5|g") {
		t.Fatalf("unexpected prefix: %s", got)
	}
	if !strings.Contains(got, "|#") || !strings.Contains(got, "env:prod") {
		t.Fatalf("missing tags: %s", got)
	}
}

func TestFormatLine_Counter(t *testing.T) {
	got := formatLine("cdn.requests", 42, "c", nil)
	if got != "cdn.requests:42|c" {
		t.Fatalf("unexpected line: %s", got)
	}
}

func TestFormatLine_Timing(t *testing.T) {
	got := formatLine("cdn.latency", 150.5, "ms", sinks.Tags{"host": "a"})
	if !strings.HasPrefix(got, "cdn.latency:150.5|ms") {
		t.Fatalf("unexpected prefix: %s", got)
	}
	if !strings.Contains(got, "host:a") {
		t.Fatalf("missing tag: %s", got)
	}
}

func TestFormatLine_MultipleTags(t *testing.T) {
	got := formatLine("m", 1, "g", sinks.Tags{"a": "1", "b": "2"})
	if !strings.Contains(got, "|#") {
		t.Fatalf("missing tag prefix: %s", got)
	}
	if !strings.Contains(got, "a:1") || !strings.Contains(got, "b:2") {
		t.Fatalf("missing tags: %s", got)
	}
}

func TestBuildName(t *testing.T) {
	s := NewSink(StatsdOpts{Namespace: "hydrolix", Subsystem: "exporter"})
	got := s.buildName("cdn.delivery.edge", "byte")
	if got != "hydrolix.exporter.cdn.delivery.edge.bytes" {
		t.Fatalf("unexpected name: %s", got)
	}
}

func TestSinkSendsUDP(t *testing.T) {
	// Start a UDP listener to capture what the sink sends
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()

	addr := pc.LocalAddr().String()
	s := NewSink(StatsdOpts{
		Namespace: "test",
		Subsystem: "sub",
		Addr:      addr,
		BaseTags:  map[string]string{"env": "test"},
	})
	s.Start()
	defer s.Stop()

	s.Gauge("metric", "byte", 99.5, sinks.Tags{"host": "a"})

	buf := make([]byte, 1024)
	pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(buf[:n])

	if !strings.HasPrefix(got, "test.sub.metric.bytes:99.5|g") {
		t.Fatalf("unexpected datagram: %s", got)
	}
	if !strings.Contains(got, "env:test") || !strings.Contains(got, "host:a") {
		t.Fatalf("missing tags: %s", got)
	}
}

func TestWithTags_Scoped(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()

	s := NewSink(StatsdOpts{
		Namespace: "ns",
		Subsystem: "ss",
		Addr:      pc.LocalAddr().String(),
	})
	s.Start()
	defer s.Stop()

	scoped := s.WithTags(sinks.Tags{"cdn": "akamai"})
	scoped.Inc("requests", "request", 1, sinks.Tags{"host": "b"})

	buf := make([]byte, 1024)
	pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(buf[:n])

	if !strings.Contains(got, "cdn:akamai") || !strings.Contains(got, "host:b") {
		t.Fatalf("scoped tags missing: %s", got)
	}
}

func TestTiming(t *testing.T) {
	got := formatLine("cdn.latency.seconds", 250, "ms", nil)
	if got != "cdn.latency.seconds:250|ms" {
		t.Fatalf("unexpected: %s", got)
	}
}
