// Package prometheus provides a Prometheus-backed MetricSink with an optional
// self-hosted /metrics HTTP server controlled via Start()/Stop().
package prometheus

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/mercereau/hydrolix-metrics-go/internal/sinks"
)

// ---------- Core registry & vectors (shared across scopes) ----------
type promCore struct {
	reg  *prom.Registry
	opts PromOpts
	mu   sync.RWMutex

	counters      map[string]*prom.CounterVec
	counterLbls   map[string][]string
	gauges        map[string]*prom.GaugeVec
	gaugeLbls     map[string][]string
	histograms    map[string]*prom.HistogramVec
	histogramLbls map[string][]string

	// HTTP server state
	srvMu   sync.Mutex
	srv     *http.Server
	ln      net.Listener
	running bool
	srvWg   sync.WaitGroup
}

// PromScoped is the user-facing handle (implements MetricSink) and can also start/stop its own server.
type PromScoped struct {
	p    *promCore
	base sinks.Tags
}

// PromOpts controls registration, histogram buckets, and the optional HTTP server.
type PromOpts struct {
	Namespace            string
	Subsystem            string
	HistogramBuckets     []float64
	RegisterGoCollectors bool

	// Optional embedded HTTP server for /metrics (Start/Stop)
	ListenAddr      string        // default ":2112"
	MetricsPath     string        // default "/metrics"
	ReadTimeout     time.Duration // optional
	WriteTimeout    time.Duration // optional
	IdleTimeout     time.Duration // optional
	ShutdownTimeout time.Duration // default 5s
}

// NewSink returns a MetricSink that can also serve /metrics via Start().
func NewSink(opts PromOpts) *PromScoped {
	if len(opts.HistogramBuckets) == 0 {
		opts.HistogramBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
	}
	core := &promCore{
		reg:           prom.NewRegistry(),
		opts:          opts,
		counters:      map[string]*prom.CounterVec{},
		counterLbls:   map[string][]string{},
		gauges:        map[string]*prom.GaugeVec{},
		gaugeLbls:     map[string][]string{},
		histograms:    map[string]*prom.HistogramVec{},
		histogramLbls: map[string][]string{},
	}
	if opts.RegisterGoCollectors {
		core.reg.MustRegister(collectors.NewGoCollector())
		core.reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
		// optional (adds myapp_build_info{version,revision} 1)
		// core.reg.MustRegister(collectors.NewBuildInfoCollector())
	}
	return &PromScoped{p: core, base: nil}
}

// Start launches the self-hosted HTTP server exposing /sinks.
// It returns immediately; errors are swallowed by design. Call Stop() to shutdown.
func (s *PromScoped) Start() {
	s.p.startHTTP()
	slog.Info("Prometheus sink HTTP server started")
}

// Stop gracefully shuts down the self-hosted HTTP server (if running).
func (s *PromScoped) Stop() {
	s.p.stopHTTP()
	slog.Info("Prometheus sink HTTP server stopped")
}

// WithTimestamp is accepted for interface compatibility; Prometheus ignores per-sample timestamps.
func (s *PromScoped) WithTimestamp(int64) sinks.MetricSink {
	return &PromScoped{p: s.p, base: s.base}
}

func (s *PromScoped) WithTags(t sinks.Tags) sinks.MetricSink {
	return &PromScoped{p: s.p, base: mergeTags(s.base, t)}
}

// Handler returns a http.Handler that serves metrics for embedding in your own server.
func (s *PromScoped) Handler() http.Handler {
	return promhttp.HandlerFor(s.p.reg, promhttp.HandlerOpts{})
}

// Metric implementations -------------------------------------------------------

func (s *PromScoped) Inc(name, unit string, value float64, tags sinks.Tags) {
	if value <= 0 {
		return // Prom counters must not decrement; ignore non-positive
	}
	mName := s.p.counterName(name, unit)
	lblNames := s.p.labelsFor(mName, mergeTags(s.base, tags))
	cv := s.p.getOrCreateCounter(mName, lblNames)
	cv.With(labelValues(lblNames, mergeTags(s.base, tags))).Add(value)
}

func (s *PromScoped) Rate(name, unit string, value float64, tags sinks.Tags) {
	// Record as a counter; compute rate in PromQL: rate(<name>_total[5m])
	s.Inc(name, unit, value, tags)
}

func (s *PromScoped) Gauge(name, unit string, value float64, tags sinks.Tags) {
	mName := s.p.gaugeName(name, unit)
	lblNames := s.p.labelsFor(mName, mergeTags(s.base, tags))
	gv := s.p.getOrCreateGauge(mName, lblNames)
	gv.With(labelValues(lblNames, mergeTags(s.base, tags))).Set(value)
}

func (s *PromScoped) Timing(name string, d time.Duration, tags sinks.Tags) {
	// Observe seconds in a histogram
	mName := s.p.histoName(name, "seconds")
	lblNames := s.p.labelsFor(mName, mergeTags(s.base, tags))
	hv := s.p.getOrCreateHistogram(mName, lblNames)
	hv.With(labelValues(lblNames, mergeTags(s.base, tags))).Observe(d.Seconds())
}

// ---------- Core registry & vectors (shared across scopes) ----------

func (p *promCore) fqName(raw string) string {
	base := sanitizeMetricName(raw)
	ns := sanitizeNS(p.opts.Namespace)
	ss := sanitizeNS(p.opts.Subsystem)
	switch {
	case ns != "" && ss != "":
		return ns + "_" + ss + "_" + base
	case ns != "":
		return ns + "_" + base
	case ss != "":
		return ss + "_" + base
	default:
		return base
	}
}

func (p *promCore) counterName(name, unit string) string {
	s := p.fqName(name)
	if !strings.HasSuffix(s, "_total") {
		s += "_total"
	}
	if unit = sanitizeUnit(unit); unit != "" && !strings.HasSuffix(s, "_"+unit) {
		s += "_" + unit
	}
	return s
}
func (p *promCore) gaugeName(name, unit string) string {
	s := p.fqName(name)
	if unit = sanitizeUnit(unit); unit != "" && !strings.HasSuffix(s, "_"+unit) {
		s += "_" + unit
	}
	return s
}
func (p *promCore) histoName(name, unit string) string {
	// Histograms are not *_total; they export multiple series (_bucket/_sum/_count).
	return p.gaugeName(name, unit)
}

// labelsFor establishes the allowed label keys for a metric name (first call wins).
// Later calls with extra keys are ignored; missing keys are set to "".
func (p *promCore) labelsFor(metricName string, tags sinks.Tags) []string {
	keys := sortedKeys(tags)
	p.mu.Lock()
	defer p.mu.Unlock()

	if l, ok := p.counterLbls[metricName]; ok {
		return l
	}
	if l, ok := p.gaugeLbls[metricName]; ok {
		return l
	}
	if l, ok := p.histogramLbls[metricName]; ok {
		return l
	}
	// First time: store set (same for any vec type with this name)
	p.counterLbls[metricName] = keys
	p.gaugeLbls[metricName] = keys
	p.histogramLbls[metricName] = keys
	return keys
}

func (p *promCore) getOrCreateCounter(name string, labelNames []string) *prom.CounterVec {
	p.mu.RLock()
	cv := p.counters[name]
	p.mu.RUnlock()
	if cv != nil {
		return cv
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if cv = p.counters[name]; cv != nil {
		return cv
	}
	cv = prom.NewCounterVec(prom.CounterOpts{
		Name: name,
		Help: name,
	}, labelNames)
	p.reg.MustRegister(cv)
	p.counters[name] = cv
	return cv
}

func (p *promCore) getOrCreateGauge(name string, labelNames []string) *prom.GaugeVec {
	p.mu.RLock()
	gv := p.gauges[name]
	p.mu.RUnlock()
	if gv != nil {
		return gv
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if gv = p.gauges[name]; gv != nil {
		return gv
	}
	gv = prom.NewGaugeVec(prom.GaugeOpts{
		Name: name,
		Help: name,
	}, labelNames)
	p.reg.MustRegister(gv)
	p.gauges[name] = gv
	return gv
}

func (p *promCore) getOrCreateHistogram(name string, labelNames []string) *prom.HistogramVec {
	p.mu.RLock()
	hv := p.histograms[name]
	p.mu.RUnlock()
	if hv != nil {
		return hv
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if hv = p.histograms[name]; hv != nil {
		return hv
	}
	hv = prom.NewHistogramVec(prom.HistogramOpts{
		Name:    name,
		Help:    name,
		Buckets: p.opts.HistogramBuckets,
	}, labelNames)
	p.reg.MustRegister(hv)
	p.histograms[name] = hv
	return hv
}

// ---------- HTTP server management ----------

func (p *promCore) startHTTP() {
	p.srvMu.Lock()
	defer p.srvMu.Unlock()
	if p.running {
		return
	}
	// defaults
	addr := p.opts.ListenAddr
	if addr == "" {
		addr = ":2112"
	}
	path := p.opts.MetricsPath
	if path == "" {
		path = "/metrics"
	}
	sdTO := p.opts.ShutdownTimeout
	if sdTO == 0 {
		sdTO = 5 * time.Second
	}

	// mux with /metrics
	mux := http.NewServeMux()
	mux.Handle(path, promhttp.HandlerFor(p.reg, promhttp.HandlerOpts{}))

	// listener
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// best-effort: do not mark running if we failed to bind
		return
	}

	// server
	srv := &http.Server{
		Handler:      mux,
		ReadTimeout:  p.opts.ReadTimeout,
		WriteTimeout: p.opts.WriteTimeout,
		IdleTimeout:  p.opts.IdleTimeout,
		// Addr is not required when serving on a Listener
	}

	p.ln = ln
	p.srv = srv
	p.running = true
	p.srvWg.Add(1)
	go func() {
		defer p.srvWg.Done()
		_ = srv.Serve(ln) // returns on Shutdown/Close; ignore error
		_ = sdTO          // keep sdTO referenced; actual use is in stopHTTP
	}()
}

func (p *promCore) stopHTTP() {
	p.srvMu.Lock()
	if !p.running {
		p.srvMu.Unlock()
		return
	}
	srv := p.srv
	ln := p.ln
	p.srv = nil
	p.ln = nil
	p.running = false
	sdTO := p.opts.ShutdownTimeout
	if sdTO == 0 {
		sdTO = 5 * time.Second
	}
	p.srvMu.Unlock()

	// graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), sdTO)
	defer cancel()
	_ = srv.Shutdown(ctx) // best-effort; ignore error
	_ = ln.Close()        // in case Shutdown didn't close it
	p.srvWg.Wait()
}

// ---------- utils ----------

func mergeTags(a, b sinks.Tags) sinks.Tags {
	if len(a) == 0 {
		out := make(sinks.Tags, len(b))
		for k, v := range b {
			out[sanitizeLabelKey(k)] = v
		}
		return out
	}
	out := make(sinks.Tags, len(a)+len(b))
	for k, v := range a {
		out[sanitizeLabelKey(k)] = v
	}
	for k, v := range b {
		out[sanitizeLabelKey(k)] = v
	}
	return out
}

func sortedKeys(t sinks.Tags) []string {
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, sanitizeLabelKey(k))
	}
	sort.Strings(keys)
	return keys
}

func labelValues(keys []string, t sinks.Tags) prom.Labels {
	lbls := make(prom.Labels, len(keys))
	for _, k := range keys {
		lbls[k] = t[k] // missing → "", OK
	}
	return lbls
}

var (
	reMetric = regexp.MustCompile(`[^a-zA-Z0-9_:]`)
	reNS     = regexp.MustCompile(`[^a-zA-Z0-9_]`)
	reLabelK = regexp.MustCompile(`[^a-zA-Z0-9_]`)
)

func sanitizeMetricName(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, ".", "_")
	s = reMetric.ReplaceAllString(s, "_")
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		s = "m_" + s
	}
	return s
}
func sanitizeUnit(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	u = strings.ReplaceAll(u, "/", "_per_")
	u = strings.ReplaceAll(u, "-", "_")
	return reLabelK.ReplaceAllString(u, "_")
}
func sanitizeNS(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return reNS.ReplaceAllString(s, "_")
}
func sanitizeLabelKey(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	k = reLabelK.ReplaceAllString(k, "_")
	if k == "" || (k[0] >= '0' && k[0] <= '9') {
		k = "l_" + k
	}
	return k
}
