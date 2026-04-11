package hydrolix

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/mercereau/hydrolix-metrics-go/internal/sinks"
)

func TestLoadEmbeddedConfig(t *testing.T) {
	cfg, err := LoadConfig("", testConfigsFS())
	if err != nil {
		t.Fatalf("LoadConfig (embedded) failed: %v", err)
	}
	if len(cfg.Queries) == 0 {
		t.Fatal("expected at least one query in embedded config")
	}
	if cfg.Defaults.OffsetStartMinutes == 0 {
		t.Fatal("expected non-zero default offset_start_minutes")
	}
	for _, q := range cfg.Queries {
		if q.Name == "" {
			t.Fatal("query name should not be empty")
		}
		if q.RenderedSQL() == "" {
			t.Fatalf("query %q: rendered SQL should not be empty", q.Name)
		}
		if contains(q.RenderedSQL(), "{{") {
			t.Fatalf("query %q: rendered SQL still contains template placeholders", q.Name)
		}
	}
}

func TestConfigOffsetOverrides(t *testing.T) {
	cfg, err := LoadConfig("", testConfigsFS())
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	cfg.Defaults.OffsetStartMinutes = 10
	cfg.Defaults.OffsetEndMinutes = 2
	cfg, err = reResolveOffsets(cfg)
	if err != nil {
		t.Fatalf("reResolveOffsets failed: %v", err)
	}
	for _, q := range cfg.Queries {
		if q.OffsetStartMinutes == nil && q.ResolvedOffsetStart() != 10 {
			t.Fatalf("query %q: expected resolved offset start 10, got %d", q.Name, q.ResolvedOffsetStart())
		}
		if q.OffsetStartMinutes == nil && !contains(q.RenderedSQL(), "10") {
			t.Fatalf("query %q: rendered SQL should contain offset 10", q.Name)
		}
	}
}

func TestConfigMergedTags(t *testing.T) {
	cfg, err := LoadConfig("", testConfigsFS())
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	for _, q := range cfg.Queries {
		merged := q.MergedTags(cfg.Defaults.Tags)
		for k, v := range cfg.Defaults.Tags {
			if merged[k] != v {
				t.Fatalf("query %q: expected default tag %s=%s, got %s", q.Name, k, v, merged[k])
			}
		}
		for k, v := range q.Tags {
			if merged[k] != v {
				t.Fatalf("query %q: expected query tag %s=%s, got %s", q.Name, k, v, merged[k])
			}
		}
	}
}

func TestEmitMetricsScalar(t *testing.T) {
	q := &QueryConfig{
		Name:            "test_scalar",
		TimestampColumn: "time",
		Metrics: []MetricConfig{
			{Column: "value", Name: "test.metric", Unit: "request", Type: "gauge"},
		},
		Dimensions: []DimensionConfig{
			{Column: "host", Tag: "hostname"},
		},
	}
	resp := &GenericResponse{
		Data: []map[string]any{
			{"time": "2024-01-15 10:30:00", "host": "example.com", "value": float64(42)},
		},
	}

	sink := newCaptureSink()
	emitMetrics(q, resp, map[string]string{"source": "test"}, sinks.MetricSinks{sink})

	if len(sink.store.gauges) != 1 {
		t.Fatalf("expected 1 gauge, got %d", len(sink.store.gauges))
	}
	g := sink.store.gauges[0]
	if g.name != "test.metric" || g.value != 42 {
		t.Fatalf("unexpected gauge: %+v", g)
	}
	if g.tags["hostname"] != "example.com" {
		t.Fatalf("missing hostname tag: %v", g.tags)
	}
	if g.tags["source"] != "test" {
		t.Fatalf("missing default tag: %v", g.tags)
	}
}

func TestEmitMetricsArray(t *testing.T) {
	q := &QueryConfig{
		Name:            "test_array",
		TimestampColumn: "time",
		Metrics: []MetricConfig{
			{
				Column:      "latency",
				Name:        "test.latency",
				Unit:        "second",
				Type:        "gauge",
				ArrayTag:    "percentile",
				ArrayValues: []string{"p50", "p99"},
			},
		},
	}
	resp := &GenericResponse{
		Data: []map[string]any{
			{"time": "2024-01-15 10:30:00", "latency": []any{float64(0.5), float64(1.2)}},
		},
	}

	sink := newCaptureSink()
	emitMetrics(q, resp, nil, sinks.MetricSinks{sink})

	if len(sink.store.gauges) != 2 {
		t.Fatalf("expected 2 gauges, got %d", len(sink.store.gauges))
	}
	if sink.store.gauges[0].tags["percentile"] != "p50" {
		t.Fatalf("expected p50 tag, got %v", sink.store.gauges[0].tags)
	}
	if sink.store.gauges[1].tags["percentile"] != "p99" {
		t.Fatalf("expected p99 tag, got %v", sink.store.gauges[1].tags)
	}
}

func TestEmitMetricsDimensionFormat(t *testing.T) {
	q := &QueryConfig{
		Name:            "test_format",
		TimestampColumn: "time",
		Dimensions: []DimensionConfig{
			{Column: "code", Tag: "status_code", Format: "%d"},
		},
		Metrics: []MetricConfig{
			{Column: "count", Name: "test.count", Unit: "error", Type: "counter"},
		},
	}
	resp := &GenericResponse{
		Data: []map[string]any{
			{"time": "2024-01-15 10:30:00", "code": float64(404), "count": float64(5)},
		},
	}

	sink := newCaptureSink()
	emitMetrics(q, resp, nil, sinks.MetricSinks{sink})

	if len(sink.store.incs) != 1 {
		t.Fatalf("expected 1 counter, got %d", len(sink.store.incs))
	}
	if sink.store.incs[0].tags["status_code"] != "404" {
		t.Fatalf("expected status_code=404, got %v", sink.store.incs[0].tags)
	}
}

func TestConvertTimeValidAndInvalid(t *testing.T) {
	got := convertTime("2024-01-15 10:30:00")
	if got == 0 {
		t.Fatalf("expected valid unix time for valid input, got %d", got)
	}
	if got < 1704067200 || got > 1735689600 {
		t.Fatalf("expected timestamp in 2024 range, got %d", got)
	}

	invalidGot := convertTime("not-a-time")
	if invalidGot == 0 {
		t.Fatalf("expected non-zero fallback for invalid time, got %d", invalidGot)
	}
}

func TestUpdateTokenAndQueryWithTLSServer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/config/v1/login/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(loginResponse{AccessToken: "test-token"})
	})
	mux.HandleFunc("/query/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	orig := http.DefaultTransport
	if tr, ok := orig.(*http.Transport); ok {
		clone := tr.Clone()
		if clone.TLSClientConfig == nil {
			clone.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		} else {
			clone.TLSClientConfig.InsecureSkipVerify = true
		}
		http.DefaultTransport = clone
		defer func() { http.DefaultTransport = orig }()
	}

	host := srv.Listener.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := &Client{
		opts:       HydrolixOpts{Host: host, Username: "u", Password: "p"},
		httpClient: &http.Client{},
		ctx:        ctx,
	}

	if err := c.UpdateToken(context.Background()); err != nil {
		t.Fatalf("UpdateToken failed: %v", err)
	}
	if c.token != "test-token" {
		t.Fatalf("expected token to be set to test-token, got %q", c.token)
	}

	body, err := c.Query("select 1")
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if body == "" || body == "null" {
		t.Fatalf("unexpected empty body from Query: %q", body)
	}

	_ = os.Setenv("TZ", "UTC")
}

// ---------- Test helpers ----------

// testConfigsFS returns an os.DirFS pointing at the project root so tests
// can read configs/ the same way the embedded FS does at runtime.
func testConfigsFS() fs.FS {
	// Tests run from the package directory; walk up to the repo root.
	return os.DirFS("../../..")
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

type metricCapture struct {
	name  string
	unit  string
	value float64
	tags  map[string]string
}

// captureStore is shared state across all scoped views of a captureSink.
type captureStore struct {
	gauges []metricCapture
	incs   []metricCapture
}

// captureSink implements sinks.MetricSink for testing.
type captureSink struct {
	store *captureStore
	ts    int64
	base  sinks.Tags
}

func newCaptureSink() *captureSink {
	return &captureSink{store: &captureStore{}}
}

func (s *captureSink) Start() {}
func (s *captureSink) Stop()  {}

func (s *captureSink) Gauge(name, unit string, value float64, tags sinks.Tags) {
	s.store.gauges = append(s.store.gauges, metricCapture{name: name, unit: unit, value: value, tags: copyTags(tags)})
}
func (s *captureSink) Inc(name, unit string, value float64, tags sinks.Tags) {
	s.store.incs = append(s.store.incs, metricCapture{name: name, unit: unit, value: value, tags: copyTags(tags)})
}
func (s *captureSink) Rate(name, unit string, value float64, tags sinks.Tags) {
	s.store.incs = append(s.store.incs, metricCapture{name: name, unit: unit, value: value, tags: copyTags(tags)})
}
func (s *captureSink) Timing(_ string, _ time.Duration, _ sinks.Tags) {}
func (s *captureSink) WithTags(t sinks.Tags) sinks.MetricSink {
	return &captureSink{store: s.store, ts: s.ts, base: sinks.MergeTags(s.base, t)}
}
func (s *captureSink) WithTimestamp(ts int64) sinks.MetricSink {
	return &captureSink{store: s.store, ts: ts, base: s.base}
}

func copyTags(t map[string]string) map[string]string {
	out := make(map[string]string, len(t))
	for k, v := range t {
		out[k] = v
	}
	return out
}
