// Package otel provides an OpenTelemetry-backed MetricSink.
// It exports metrics to an OTLP collector using the Go OTel SDK.
package otel

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"

	"github.com/mercereau/hydrolix-metrics-go/internal/sinks"
)

// -------- Options & construction --------

type OTelOpts struct {
	// OTLP endpoint: "localhost:4317" for gRPC, or "localhost:4318" / "http://localhost:4318" for HTTP.
	Endpoint string

	// "grpc" (default) or "http"
	Protocol string

	// Export interval for the periodic reader (default 10s).
	ExportInterval time.Duration

	// Resource info
	ServiceName    string
	ServiceVersion string
	DeploymentEnv  string            // "prod", "stage", etc.
	ResourceAttrs  map[string]string // extra resource attrs

	// If true, use insecure transport for gRPC (no TLS).
	Insecure bool

	// Temporality preference: "delta" (default) or "cumulative".
	Temporality string

	// SelfSink receives internal health metrics (gauge evictions).
	// Must NOT be this OTel sink itself — use Prometheus or Nop (default).
	SelfSink sinks.MetricSink
}

func NewOTelSink(opts OTelOpts) *OTelScoped {
	if opts.ExportInterval <= 0 {
		opts.ExportInterval = 10 * time.Second
	}
	if opts.Temporality == "" {
		opts.Temporality = "delta"
	}
	self := opts.SelfSink
	if self == nil {
		self = sinks.NewNop(nil)
	}
	core := &otelCore{
		opts:          opts,
		counters:      map[iKey]metric.Float64Counter{},
		updowns:       map[iKey]metric.Float64UpDownCounter{},
		histograms:    map[iKey]metric.Float64Histogram{},
		gaugePrev:     map[string]float64{},
		gaugePrevTime: map[string]time.Time{},
		self:          self.WithTags(sinks.Tags{"sink": "otel"}),
	}
	return &OTelScoped{c: core}
}

// -------- Sink implementation (scoped handle) --------

type OTelScoped struct {
	c    *otelCore
	base sinks.Tags
	ts   *time.Time // optional override (stored as attributes)
}

func (s *OTelScoped) Start() { s.c.start() }
func (s *OTelScoped) Stop()  { _ = s.c.stop(context.Background()) }

func (s *OTelScoped) WithTags(t sinks.Tags) sinks.MetricSink {
	return &OTelScoped{c: s.c, base: mergeTags(s.base, t), ts: s.ts}
}

func (s *OTelScoped) WithTimestamp(ts int64) sinks.MetricSink {
	t := toTime(ts) // supports sec or msec epoch
	return &OTelScoped{c: s.c, base: s.base, ts: &t}
}

func (s *OTelScoped) Inc(name, unit string, value float64, tags sinks.Tags) {
	if value <= 0 {
		return // counters are monotonic
	}
	s.c.ensureStarted()
	meter := s.c.meter

	key := iKey{name: sanitizeName(name), unit: sanitizeUnit(unit), kind: "counter"}
	inst := s.c.getCounter(meter, key)

	at := attrsFromTags(mergeTags(s.base, tags))
	if s.ts != nil {
		at = appendEventTimeAttrs(at, *s.ts)
	}
	inst.Add(context.Background(), value, metric.WithAttributes(at...))
}

func (s *OTelScoped) Rate(name, unit string, value float64, tags sinks.Tags) {
	// Record as counter; compute rate in backend/queries.
	s.Inc(name, unit, value, tags)
}

func (s *OTelScoped) Gauge(name, unit string, value float64, tags sinks.Tags) {
	// Emulate "set" using UpDownCounter: delta = new - prev for this timeseries.
	s.c.ensureStarted()
	meter := s.c.meter

	allTags := mergeTags(s.base, tags)
	seriesKey := seriesKey(sanitizeName(name), sanitizeUnit(unit), allTags)

	prev := s.c.getGaugePrev(seriesKey)
	delta := value - prev
	s.c.setGaugePrev(seriesKey, value)

	key := iKey{name: sanitizeName(name), unit: sanitizeUnit(unit), kind: "updown"}
	inst := s.c.getUpDown(meter, key)

	at := attrsFromTags(allTags)
	if s.ts != nil {
		at = appendEventTimeAttrs(at, *s.ts)
	}
	inst.Add(context.Background(), delta, metric.WithAttributes(at...))
}

func (s *OTelScoped) Timing(name string, d time.Duration, tags sinks.Tags) {
	s.c.ensureStarted()
	meter := s.c.meter

	key := iKey{name: sanitizeName(name), unit: "s", kind: "hist"}
	inst := s.c.getHistogram(meter, key)

	at := attrsFromTags(mergeTags(s.base, tags))
	if s.ts != nil {
		at = appendEventTimeAttrs(at, *s.ts)
	}
	inst.Record(context.Background(), d.Seconds(), metric.WithAttributes(at...))
}

// -------- Core: exporter, provider, instrument caches --------

type otelCore struct {
	opts OTelOpts

	mu      sync.Mutex
	started bool

	exp    sdkmetric.Exporter
	reader *sdkmetric.PeriodicReader
	mp     *sdkmetric.MeterProvider
	meter  metric.Meter

	// instruments cached by (name, unit, kind)
	insMu      sync.RWMutex
	counters   map[iKey]metric.Float64Counter
	updowns    map[iKey]metric.Float64UpDownCounter
	histograms map[iKey]metric.Float64Histogram

	// gauge state per series key
	gMu              sync.Mutex
	gaugePrev        map[string]float64
	gaugePrevTime    map[string]time.Time
	gaugeCleanupStop chan struct{}

	self sinks.MetricSink // sink for self-monitoring metrics
}

func (c *otelCore) start() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return
	}

	// Resource
	var attrs []attribute.KeyValue
	if c.opts.ServiceName != "" {
		attrs = append(attrs, semconv.ServiceNameKey.String(c.opts.ServiceName))
	}
	if c.opts.ServiceVersion != "" {
		attrs = append(attrs, semconv.ServiceVersionKey.String(c.opts.ServiceVersion))
	}
	if c.opts.DeploymentEnv != "" {
		attrs = append(attrs, semconv.DeploymentEnvironmentKey.String(c.opts.DeploymentEnv))
	}
	for k, v := range c.opts.ResourceAttrs {
		attrs = append(attrs, attribute.String(k, v))
	}
	res, _ := resource.New(context.Background(),
		resource.WithAttributes(attrs...),
		resource.WithFromEnv(), // allow OTEL_RESOURCE_ATTRIBUTES env too
	)

	// Exporter (temporality set here, not on PeriodicReader)
	var (
		exp sdkmetric.Exporter
		err error
	)
	temporal := strings.ToLower(c.opts.Temporality)
	var ts sdkmetric.TemporalitySelector
	if temporal == "cumulative" {
		ts = CumulativeTemporalitySelector
	} else {
		ts = DeltaTemporalitySelector
	}

	switch strings.ToLower(c.opts.Protocol) {
	case "http", "http/protobuf", "http_protobuf":
		clientOpts := []otlpmetrichttp.Option{}
		if c.opts.Endpoint != "" {
			clientOpts = append(clientOpts, otlpmetrichttp.WithEndpoint(c.opts.Endpoint))
		}
		clientOpts = append(clientOpts, otlpmetrichttp.WithTemporalitySelector(ts))
		exp, err = otlpmetrichttp.New(context.Background(), clientOpts...)
	default: // gRPC
		clientOpts := []otlpmetricgrpc.Option{}
		if c.opts.Endpoint != "" {
			clientOpts = append(clientOpts, otlpmetricgrpc.WithEndpoint(c.opts.Endpoint))
		}
		if c.opts.Insecure {
			clientOpts = append(clientOpts, otlpmetricgrpc.WithInsecure())
		}
		clientOpts = append(clientOpts, otlpmetricgrpc.WithTemporalitySelector(ts))
		exp, err = otlpmetricgrpc.New(context.Background(), clientOpts...)
	}
	if err != nil {
		// In your project, handle/log the error as needed.
		return
	}

	reader := sdkmetric.NewPeriodicReader(
		exp,
		sdkmetric.WithInterval(c.opts.ExportInterval),
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)
	meter := mp.Meter("cdn-metrics")

	c.exp = exp
	c.reader = reader
	c.mp = mp
	c.meter = meter
	c.gaugeCleanupStop = make(chan struct{})
	c.started = true
	go c.cleanupGaugePrev(5 * time.Minute)
}

func (c *otelCore) ensureStarted() { c.start() }

func (c *otelCore) stop(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return nil
	}
	close(c.gaugeCleanupStop)
	err := c.mp.Shutdown(ctx) // flushes and closes exporter
	c.started = false
	c.exp = nil
	c.reader = nil
	c.mp = nil
	c.meter = nil // interface zero
	return err
}

// instrument key
type iKey struct {
	name string
	unit string
	kind string // "counter", "updown", "hist"
}

func (c *otelCore) getCounter(m metric.Meter, k iKey) metric.Float64Counter {
	c.insMu.RLock()
	inst, ok := c.counters[k]
	c.insMu.RUnlock()
	if ok {
		return inst
	}
	c.insMu.Lock()
	defer c.insMu.Unlock()
	if inst, ok = c.counters[k]; ok {
		return inst
	}
	i, _ := m.Float64Counter(k.name, metric.WithUnit(k.unit), metric.WithDescription(k.name))
	c.counters[k] = i
	return i
}

func (c *otelCore) getUpDown(m metric.Meter, k iKey) metric.Float64UpDownCounter {
	c.insMu.RLock()
	inst, ok := c.updowns[k]
	c.insMu.RUnlock()
	if ok {
		return inst
	}
	c.insMu.Lock()
	defer c.insMu.Unlock()
	if inst, ok = c.updowns[k]; ok {
		return inst
	}
	i, _ := m.Float64UpDownCounter(k.name, metric.WithUnit(k.unit), metric.WithDescription(k.name))
	c.updowns[k] = i
	return i
}

func (c *otelCore) getHistogram(m metric.Meter, k iKey) metric.Float64Histogram {
	c.insMu.RLock()
	inst, ok := c.histograms[k]
	c.insMu.RUnlock()
	if ok {
		return inst
	}
	c.insMu.Lock()
	defer c.insMu.Unlock()
	if inst, ok = c.histograms[k]; ok {
		return inst
	}
	i, _ := m.Float64Histogram(k.name, metric.WithUnit(k.unit), metric.WithDescription(k.name))
	c.histograms[k] = i
	return i
}

// gauge state (set semantics using updowncounter)
func (c *otelCore) getGaugePrev(series string) float64 {
	c.gMu.Lock()
	defer c.gMu.Unlock()
	return c.gaugePrev[series]
}
func (c *otelCore) setGaugePrev(series string, v float64) {
	c.gMu.Lock()
	c.gaugePrev[series] = v
	c.gaugePrevTime[series] = time.Now()
	c.gMu.Unlock()
}

// cleanupGaugePrev removes gauge state entries that haven't been updated within ttl.
func (c *otelCore) cleanupGaugePrev(ttl time.Duration) {
	ticker := time.NewTicker(ttl / 2)
	defer ticker.Stop()
	for {
		select {
		case <-c.gaugeCleanupStop:
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-ttl)
			c.gMu.Lock()
			for k, t := range c.gaugePrevTime {
				if t.Before(cutoff) {
					delete(c.gaugePrev, k)
					delete(c.gaugePrevTime, k)
					c.self.Inc("hydrolix.sink.gauge_evictions", "total", 1, nil)
					slog.Debug("evicted stale gauge state; next observation will emit full value as delta", "series", k)
				}
			}
			c.gMu.Unlock()
		}
	}
}

// -------- helpers --------

func attrsFromTags(t sinks.Tags) []attribute.KeyValue {
	if len(t) == 0 {
		return nil
	}
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kv := make([]attribute.KeyValue, 0, len(keys))
	for _, k := range keys {
		kv = append(kv, attribute.String(sanitizeAttrKey(k), t[k]))
	}
	return kv
}

func appendEventTimeAttrs(in []attribute.KeyValue, t time.Time) []attribute.KeyValue {
	return append(in,
		attribute.Int64("event_time_unix", t.Unix()),
		attribute.Int64("event_time_ms", t.UnixMilli()),
	)
}

func seriesKey(name, unit string, t sinks.Tags) string {
	if len(t) == 0 {
		return name + "|" + unit
	}
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.Grow(len(name) + len(unit) + len(keys)*8)
	b.WriteString(name)
	b.WriteString("|")
	b.WriteString(unit)
	for _, k := range keys {
		b.WriteString("|")
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(t[k])
	}
	return b.String()
}

func mergeTags(a, b sinks.Tags) sinks.Tags {
	if len(a) == 0 {
		out := make(sinks.Tags, len(b))
		for k, v := range b {
			out[k] = v
		}
		return out
	}
	out := make(sinks.Tags, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func sanitizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, ".", "_")
	return s
}
func sanitizeUnit(u string) string {
	return strings.TrimSpace(u) // OTel accepts UCUM-like units; keep as provided
}
func sanitizeAttrKey(k string) string {
	k = strings.TrimSpace(strings.ToLower(k))
	if k == "" {
		return "attr"
	}
	return k
}

func toTime(ts int64) time.Time {
	// seconds vs milliseconds
	if ts < 1_000_000_000_000 {
		return time.Unix(ts, 0).UTC()
	}
	sec := ts / 1000
	ms := ts % 1000
	return time.Unix(sec, ms*int64(time.Millisecond)).UTC()
}

// -------- Temporality selectors (helpers for older SDKs) --------

func DeltaTemporalitySelector(kind sdkmetric.InstrumentKind) metricdata.Temporality {
	return metricdata.DeltaTemporality
}

func CumulativeTemporalitySelector(kind sdkmetric.InstrumentKind) metricdata.Temporality {
	return metricdata.CumulativeTemporality
}
