package datadog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadog"
	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
	"github.com/gertd/go-pluralize"
	metrics "github.com/mercereau/hydrolix-metrics-go/internal/sinks"
)

var pl *pluralize.Client

func init() {
	pl = pluralize.NewClient()
}

type Sink struct {
	mu   sync.RWMutex
	opts DatadogOpts
	base metrics.Tags
	self metrics.MetricSink // sink for self-monitoring metrics (never the DataDog channel)

	metricCh        chan datadogV2.MetricSeries
	stopCollectorCh chan bool
	payloadsCh      chan datadogV2.MetricPayload
	stopSenderCh    chan bool
	closed          bool
	wg              sync.WaitGroup
}

// datadogScoped is an immutable scoped view of a Sink with a fixed timestamp and base tags.
// WithTimestamp and WithTags return a new datadogScoped rather than mutating the root Sink,
// preventing races when multiple goroutines scope the same sink concurrently.
type datadogScoped struct {
	root      *Sink
	timestamp int64
	base      metrics.Tags
}

func (s *datadogScoped) Start() {}
func (s *datadogScoped) Stop()  {}
func (s *datadogScoped) WithTags(t metrics.Tags) metrics.MetricSink {
	return &datadogScoped{root: s.root, timestamp: s.timestamp, base: metrics.MergeTags(s.base, t)}
}
func (s *datadogScoped) WithTimestamp(ts int64) metrics.MetricSink {
	return &datadogScoped{root: s.root, timestamp: ts, base: s.base}
}
func (s *datadogScoped) ts() int64 {
	if s.timestamp != 0 {
		return s.timestamp
	}
	return time.Now().Unix()
}
func (s *datadogScoped) Inc(name, unit string, value float64, tags metrics.Tags) {
	m := CreateMetric(datadogV2.METRICINTAKETYPE_COUNT, s.root.BuildMetricName(name, unit), unit, s.ts(), value, metrics.MergeTags(s.base, tags))
	s.root.submitMetric(m)
}
func (s *datadogScoped) Rate(name, unit string, value float64, tags metrics.Tags) {
	m := CreateMetric(datadogV2.METRICINTAKETYPE_RATE, s.root.BuildMetricName(name, unit), unit, s.ts(), value, metrics.MergeTags(s.base, tags))
	s.root.submitMetric(m)
}
func (s *datadogScoped) Gauge(name, unit string, value float64, tags metrics.Tags) {
	m := CreateMetric(datadogV2.METRICINTAKETYPE_GAUGE, s.root.BuildMetricName(name, unit), unit, s.ts(), value, metrics.MergeTags(s.base, tags))
	s.root.submitMetric(m)
}
func (s *datadogScoped) Timing(_ string, _ time.Duration, _ metrics.Tags) {}

type DatadogOpts struct {
	Namespace string
	Subsystem string
	BaseTags  map[string]string

	FlushInterval  time.Duration
	QueueSize      int
	BatchSize      int
	MaxRetries     int
	InitialBackoff time.Duration
	Concurrency    uint16

	// SelfSink receives internal health metrics (drops, retries, failures).
	// Must NOT be the DataDog sink itself — use Prometheus or Nop (default).
	SelfSink metrics.MetricSink
}

func NewSink(o DatadogOpts) *Sink {
	self := o.SelfSink
	if self == nil {
		self = metrics.NewNop(nil)
	}
	client := &Sink{
		opts:            o,
		base:            o.BaseTags,
		self:            self.WithTags(metrics.Tags{"sink": "datadog"}),
		metricCh:        make(chan datadogV2.MetricSeries, o.BatchSize*int(o.Concurrency)*2),
		payloadsCh:      make(chan datadogV2.MetricPayload, o.QueueSize),
		stopCollectorCh: make(chan bool),
		stopSenderCh:    make(chan bool),
	}
	return client
}

func CreateMetric(typ datadogV2.MetricIntakeType, name, unit string, timestamp int64, value float64, tags metrics.Tags) datadogV2.MetricSeries {
	t, _ := datadogV2.NewMetricIntakeTypeFromValue(int32(typ))
	m := datadogV2.MetricSeries{
		Metric: name,
		Unit:   &unit,
		Type:   t,
		Points: []datadogV2.MetricPoint{
			{
				Timestamp: &timestamp,
				Value:     &value,
			},
		},
		Tags: flattenTags(tags),
	}
	return m
}

func (dd *Sink) WithTimestamp(ts int64) metrics.MetricSink {
	return &datadogScoped{root: dd, timestamp: ts, base: dd.base}
}

func (dd *Sink) BuildMetricName(metric, unit string) string {
	unit = pl.Plural(unit)
	return fmt.Sprintf("%s.%s.%s.%s", dd.opts.Namespace, dd.opts.Subsystem, metric, unit)
}

func (dd *Sink) Inc(name, unit string, value float64, tags metrics.Tags) {
	m := CreateMetric(datadogV2.METRICINTAKETYPE_COUNT, dd.BuildMetricName(name, unit), unit, time.Now().Unix(), value, tags)
	dd.submitMetric(m)
}

func (dd *Sink) Rate(name, unit string, value float64, tags metrics.Tags) {
	m := CreateMetric(datadogV2.METRICINTAKETYPE_RATE, dd.BuildMetricName(name, unit), unit, time.Now().Unix(), value, tags)
	dd.submitMetric(m)
}

func (dd *Sink) Gauge(name string, unit string, value float64, tags metrics.Tags) {
	m := CreateMetric(datadogV2.METRICINTAKETYPE_GAUGE, dd.BuildMetricName(name, unit), unit, time.Now().Unix(), value, tags)
	dd.submitMetric(m)
}

func (dd *Sink) submitMetric(m datadogV2.MetricSeries) {
	if dd.closed {
		return
	}
	select {
	case dd.metricCh <- m:
	default:
		dd.self.Inc("hydrolix.sink.metrics_dropped", "total", 1, nil)
		slog.Error("Dropping metric: buffer full")
	}
}

func (dd *Sink) Timing(_ string, _ time.Duration, _ metrics.Tags) {}

func (dd *Sink) WithTags(t metrics.Tags) metrics.MetricSink {
	return &datadogScoped{root: dd, base: metrics.MergeTags(dd.base, t)}
}

func flattenTags(tags metrics.Tags) (flat []string) {
	for k, v := range tags {
		flat = append(flat, fmt.Sprintf("%s:%s", k, v))
	}
	return flat
}

type SeriesList []datadogV2.MetricSeries

func (s SeriesList) String() string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode([]datadogV2.MetricSeries(s)); err != nil {
		// Fallback if JSON fails
		return fmt.Sprintf("%#v", []datadogV2.MetricSeries(s))
	}
	return b.String()
}

func (dd *Sink) Stop() {
	dd.mu.Lock()
	if dd.closed {
		dd.mu.Unlock()
		return
	}
	dd.closed = true
	dd.mu.Unlock()

	slog.Info("Stopping Datadog sink...")
	dd.stopCollector()
	slog.Info("Stopped collectors, now stopping sender...")
	dd.stopSender()
	slog.Info("Waiting for Datadog sink goroutines to finish...")
	dd.wg.Wait()
	slog.Info("Datadog sink closed")
	close(dd.metricCh)
}

func (dd *Sink) stopCollector() {
	dd.mu.Lock()
	defer dd.mu.Unlock()
	dd.stopCollectorCh <- true
	close(dd.stopCollectorCh)
}

func (dd *Sink) Start() {
	dd.wg.Add(1)
	go dd.runSender()
	for i := uint16(0); i < dd.opts.Concurrency; i++ {
		dd.wg.Add(1)
		go dd.runCollector()
	}
}

func (dd *Sink) runSender() {
	defer dd.wg.Done()
	for {
		select {
		case <-dd.stopSenderCh:
			// drain payloadsCh
			for {
				select {
				case p := <-dd.payloadsCh:
					_ = dd.sendToDatadog(p)
				default:
					return // done draining
				}
			}
		case p := <-dd.payloadsCh:
			_ = dd.sendToDatadog(p)
		}
	}
}

func (dd *Sink) stopSender() {
	dd.mu.Lock()
	defer dd.mu.Unlock()
	dd.stopSenderCh <- true
	close(dd.stopSenderCh)
}

func (dd *Sink) runCollector() {
	defer dd.wg.Done()
	buffer := make([]datadogV2.MetricSeries, 0, dd.opts.BatchSize)
	timer := time.NewTimer(dd.opts.FlushInterval)
	for {
		select {
		case <-dd.stopCollectorCh:
			dd.flush(buffer)
			slog.Info("Collector stopping")
			return
		case m := <-dd.metricCh:
			buffer = append(buffer, m)
			if len(buffer) >= dd.opts.BatchSize {
				dd.flush(buffer)
				buffer = buffer[:0]
				resetTimer(timer, dd.opts.FlushInterval)
			}
		case <-timer.C:
			if len(buffer) > 0 {
				dd.flush(buffer)
				buffer = buffer[:0]
			}
			resetTimer(timer, dd.opts.FlushInterval)
		}
	}
}

func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

func (dd *Sink) flush(series []datadogV2.MetricSeries) {
	if len(series) == 0 {
		return
	}
	slog.Debug("Flush payload queue")

	payload := datadogV2.MetricPayload{Series: series}
	err := dd.queuePayload(payload)
	if err != nil {
		slog.Error("Failed to add payload to queue", "error", err)
	}
}

var ErrSinkClosed = errors.New("sink closed")

func (dd *Sink) queuePayload(p datadogV2.MetricPayload) error {
	dd.mu.RLock()
	closed := dd.closed
	dd.mu.RUnlock()

	if closed {
		return ErrSinkClosed
	}

	select {
	case dd.payloadsCh <- p:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("timeout queueing payload")
	}
}

func (dd *Sink) sendToDatadog(payload datadogV2.MetricPayload) error {
	for attempt := 0; attempt < dd.opts.MaxRetries; attempt++ {
		if attempt > 0 {
			dd.self.Inc("hydrolix.sink.send_retries", "total", 1, nil)
		}

		ctx := datadog.NewDefaultContext(context.Background())
		configuration := datadog.NewConfiguration()
		apiClient := datadog.NewAPIClient(configuration)
		api := datadogV2.NewMetricsApi(apiClient)
		resp, r, err := api.SubmitMetrics(ctx, payload, *datadogV2.NewSubmitMetricsOptionalParameters())

		if err == nil && r.StatusCode < 300 {
			dd.self.Inc("hydrolix.sink.payloads_sent", "total", 1, nil)
			slog.Debug(fmt.Sprintf("Sent Metrics: %d, Status Code: %v", len(payload.Series), r.StatusCode))
			if len(resp.Errors) > 0 {
				slog.Error(fmt.Sprintf("Errors in response: %s", resp.Errors))
			}
			return nil
		}

		slog.Error(fmt.Sprintf("Error when calling `MetricsApi.SubmitMetrics`: %v", err))
		slog.Error(fmt.Sprintf("Full HTTP response: %v", r))

		responseContent, _ := json.MarshalIndent(resp, "", "  ")
		slog.Debug(fmt.Sprintf("Response from `MetricsApi.SubmitMetrics`:\n%s", responseContent))

		sleep := backoffDuration(attempt, float64(dd.opts.InitialBackoff))
		time.Sleep(sleep)
	}

	dd.self.Inc("hydrolix.sink.send_failures", "total", 1, nil)
	return errors.New("all retry attempts failed")
}

// exponential backoff
func backoffDuration(attempt int, backoff float64) time.Duration {
	jitter := rand.Float64() + 0.5
	return time.Duration(backoff * math.Pow(2, float64(attempt)) * jitter)
}
