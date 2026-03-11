package hydrolix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"gitlab-master.nvidia.com/it/sre/cdn/observability/hydrolix-collector/internal/common"
	"gitlab-master.nvidia.com/it/sre/cdn/observability/hydrolix-collector/internal/sinks"
)

type Client struct {
	name       string
	token      string
	sinks      sinks.MetricSinks
	selfSink   sinks.MetricSink // scoped sink for collector self-metrics
	opts       HydrolixOpts
	httpClient *http.Client

	mu      sync.RWMutex
	closeCh chan struct{}
	closed  bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

type HydrolixOpts struct {
	Host            string
	Username        string
	Password        string
	Token           string
	IntervalSeconds time.Duration // Polling interval duration (e.g., 15 * time.Second)
}

type loginRequest struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}
type loginResponse struct {
	AccessToken string `json:"access_token"`
}

func New(name string, o HydrolixOpts, ms ...sinks.MetricSink) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		name:       name,
		opts:       o,
		closeCh:    make(chan struct{}),
		sinks:      ms,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		ctx:        ctx,
		cancel:     cancel,
	}
	c.selfSink = c.sinks.WithTags(sinks.Tags{"component": "hydrolix-collector"})
	if o.Username != "" && o.Password != "" && o.Host != "" {
		slog.Info("Initializing client with username/password")
		if err := c.UpdateToken(context.Background()); err != nil {
			slog.Error("Failed to retrieve token", "error", err)
		}
		// Start token refresh goroutine (will be stopped in Stop())
		c.wg.Add(1)
		go c.refreshToken()
	} else if o.Token != "" && o.Host != "" {
		slog.Info("Using token instead of username/password")
		c.token = o.Token
	} else {
		slog.Info("Trying environment variables for Hydrolix client")
		var err error
		o.Host, err = common.GetEnvValue("HDX_HOST")
		if err == nil {
			slog.Info(fmt.Sprintf("Using host from environment variable HDX_HOST: %s", o.Host))
		}

		o.Token, err = common.GetEnvValue("HDX_TOKEN")
		if err == nil {
			slog.Info("Using token from environment variable HDX_TOKEN")
		} else {
			slog.Info(fmt.Sprintf("Unable to read HDX_TOKEN: %s", err.Error()))
			slog.Info("Falling back to username/password from environment variables")
			o.Username, err = common.GetEnvValue("HDX_USERNAME")
			if err != nil {
				slog.Error(err.Error())
				return nil
			}
			o.Password, err = common.GetEnvValue("HDX_PASSWORD")
			if err != nil {
				slog.Error(err.Error())
			}
		}
	}
	if o.Host == "" && (o.Token == "" && (o.Username == "" && o.Password == "")) {
		slog.Error("No authentication method provided for Hydrolix Client")
		return nil
	}
	c.opts = o
	c.token = o.Token
	return c
}

func (c *Client) refreshToken() {
	defer c.wg.Done()

	// Token refresh should happen more frequently than polling
	// Typically tokens expire after hours, so refresh every hour or use a configurable interval
	refreshInterval := 1 * time.Hour
	if c.opts.IntervalSeconds > 0 && c.opts.IntervalSeconds < refreshInterval {
		// If polling is less frequent, use that as a baseline
		refreshInterval = c.opts.IntervalSeconds
	}

	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			slog.Info("Token refresh goroutine stopping")
			return
		case <-ticker.C:
			if err := c.UpdateToken(c.ctx); err != nil {
				slog.Warn("Token could not be refreshed", "error", err)
			} else {
				slog.Debug("Token refreshed successfully")
			}
		}
	}
}

func (c *Client) Register(ms sinks.MetricSinks) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sinks = ms
	return nil
}

func (c *Client) Start() {
	// Track this goroutine in WaitGroup
	c.wg.Add(1)
	defer c.wg.Done()

	// Start the sinks
	for _, m := range c.sinks {
		m.Start()
	}

	// Use the configured polling interval
	pollingInterval := c.opts.IntervalSeconds
	if pollingInterval <= 0 {
		pollingInterval = 15 * time.Second
		slog.Warn("Invalid polling interval, using default", "default", pollingInterval)
	}

	ticker := time.NewTicker(pollingInterval)
	defer ticker.Stop()

	slog.Info("Starting Hydrolix poller", "interval", pollingInterval)

	// Poll immediately on start
	c.getMetrics()

	// Main loop
	for {
		select {
		case <-c.ctx.Done():
			slog.Info("Hydrolix poller stopping")
			return
		case <-ticker.C:
			c.getMetrics()
		}
	}
}

// Stop signals all listeners that we're shutting down.
func (c *Client) Stop() {
	slog.Info("Stopping Hydrolix client")
	c.mu.Lock()

	// Prevent multiple calls to Stop
	if c.closed {
		c.mu.Unlock()
		slog.Warn("Hydrolix client already stopped")
		return
	}
	c.closed = true
	c.mu.Unlock()

	// Cancel context to stop all goroutines
	c.cancel()

	// Wait for all goroutines to finish with timeout
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("All goroutines stopped gracefully")
	case <-time.After(10 * time.Second):
		slog.Warn("Timeout waiting for goroutines to stop, forcing shutdown")
	}

	// Close the done channel
	close(c.closeCh)

	// Stop all sinks
	for _, m := range c.sinks {
		m.Stop()
		slog.Debug("Stopped sink")
	}
	slog.Info("Hydrolix client stopped")
}

// Done returns a read-only channel that is closed on shutdown.
func (c *Client) done() <-chan struct{} {
	return c.closeCh
}

func (c *Client) getMetrics() {
	// Use a WaitGroup to ensure all polling completes before next tick
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		c.PollAkamaiEdgeRequests()
	}()

	go func() {
		defer wg.Done()
		c.PollAkamaiQuantiles()
	}()

	go func() {
		defer wg.Done()
		c.PollAkamaiErrors()
	}()

	// Wait for all polls to complete or context cancellation
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All polls completed normally
	case <-c.ctx.Done():
		// Context cancelled, don't wait for polls to finish
		slog.Debug("Context cancelled while waiting for polls to complete")
	}
}

func (c *Client) QueryHydrolix(resp any, sql string) error {
	er, err := c.Query(sql)
	if err != nil {
		slog.Error(fmt.Sprintf("Could not read Hydrolix API response: %s", err.Error()))
		return err
	}
	slog.Debug("Queried Hydrolix API")

	// Unmarshalling into a struct
	err = json.Unmarshal([]byte(er), &resp)
	if err != nil {
		slog.Error(fmt.Sprintf("Could not unmarshal request from Hydrolix API: %s", err.Error()))
		return err
	}
	return nil
}

func (c *Client) PollAkamaiEdgeRequests() {
	// Check if context is cancelled before starting work
	select {
	case <-c.ctx.Done():
		slog.Debug("Skipping Akamai Edge Requests poll - context cancelled")
		return
	default:
	}

	pollTags := sinks.Tags{"query": "edge_requests"}
	stopTimer := sinks.StartTimer(c.selfSink, "hydrolix.collector.poll.duration", pollTags)
	defer stopTimer()

	slog.Debug("Polling Akamai Edge Requests data")
	var r AkamaiEdgeRequestsResponse
	if err := c.QueryHydrolix(&r, FormattedSQLAkamaiEdgeRequests); err != nil {
		slog.Error("Failed to query Akamai edge requests", "error", err)
		c.selfSink.Inc("hydrolix.collector.poll", "total", 1, sinks.MergeTags(pollTags, sinks.Tags{"status": "error"}))
		return
	}
	c.selfSink.Inc("hydrolix.collector.poll", "total", 1, sinks.MergeTags(pollTags, sinks.Tags{"status": "success"}))
	c.selfSink.Gauge("hydrolix.collector.poll.rows", "row", float64(len(r.Data)), pollTags)

	for _, v := range r.Data {
		// Check if context is cancelled during processing
		select {
		case <-c.ctx.Done():
			slog.Debug("Context cancelled during Akamai Edge Requests processing")
			return
		default:
		}

		// Skip rows with nil required fields
		if v.TimeMinute == nil || v.Hostname == nil || v.StatusClass == nil {
			slog.Warn("Skipping row with nil required fields")
			continue
		}

		tags := make(map[string]string, 6)
		tags["cdn"] = "akamai"
		tags["source"] = "hydrolix"
		tags["hostname"] = *v.Hostname
		tags["status_class"] = *v.StatusClass

		if v.Cacheability != nil {
			tags["cacheability"] = fmt.Sprintf("%d", *v.Cacheability)
		}
		if v.CacheStatus != nil {
			tags["cache_status"] = fmt.Sprintf("%d", *v.CacheStatus)
		}

		ts := convertTime(*v.TimeMinute)
		scopedSinks := c.sinks.WithTimestamp(ts)

		if v.EdgeBytesTotal != nil {
			scopedSinks.Gauge("cdn.delivery.edge", "byte", *v.EdgeBytesTotal, tags)
		}
		if v.EdgeRequestTotal != nil {
			scopedSinks.Gauge("cdn.delivery.edge", "request", *v.EdgeRequestTotal, tags)
		}
		if v.OriginBytesTotal != nil {
			scopedSinks.Gauge("cdn.delivery.origin", "byte", *v.OriginBytesTotal, tags)
		}
		if v.OriginRequestTotal != nil {
			scopedSinks.Gauge("cdn.delivery.origin", "request", *v.OriginRequestTotal, tags)
		}
	}
	slog.Debug("Completed Akamai Edge Requests polling", "rows", len(r.Data))
}

func (c *Client) PollAkamaiErrors() {
	// Check if context is cancelled before starting work
	select {
	case <-c.ctx.Done():
		slog.Debug("Skipping Akamai Errors poll - context cancelled")
		return
	default:
	}

	pollTags := sinks.Tags{"query": "errors"}
	stopTimer := sinks.StartTimer(c.selfSink, "hydrolix.collector.poll.duration", pollTags)
	defer stopTimer()

	slog.Debug("Polling Akamai Errors data")
	var r AkamaiErrorsResponse
	if err := c.QueryHydrolix(&r, FormattedSQLAkamaiErrors); err != nil {
		slog.Error("Failed to query Akamai errors", "error", err)
		c.selfSink.Inc("hydrolix.collector.poll", "total", 1, sinks.MergeTags(pollTags, sinks.Tags{"status": "error"}))
		return
	}
	c.selfSink.Inc("hydrolix.collector.poll", "total", 1, sinks.MergeTags(pollTags, sinks.Tags{"status": "success"}))
	c.selfSink.Gauge("hydrolix.collector.poll.rows", "row", float64(len(r.Data)), pollTags)

	for _, v := range r.Data {
		// Check if context is cancelled during processing
		select {
		case <-c.ctx.Done():
			slog.Debug("Context cancelled during Akamai Errors processing")
			return
		default:
		}

		// Skip rows with nil required fields
		if v.TimeMinute == nil || v.Hostname == nil || v.StatusClass == nil {
			slog.Warn("Skipping error row with nil required fields")
			continue
		}

		tags := make(map[string]string, 6)
		tags["cdn"] = "akamai"
		tags["source"] = "hydrolix"
		tags["hostname"] = *v.Hostname
		tags["status_class"] = *v.StatusClass

		if v.CacheStatus != nil {
			tags["cache_status"] = fmt.Sprintf("%d", *v.CacheStatus)
		}
		if v.StatusCode != nil {
			tags["status_code"] = fmt.Sprintf("%d", *v.StatusCode)
		}

		ts := convertTime(*v.TimeMinute)
		scopedSinks := c.sinks.WithTimestamp(ts)

		if v.EdgeErrorsTotal != nil {
			scopedSinks.Gauge("cdn.delivery.edge", "error", *v.EdgeErrorsTotal, tags)
		}
		if v.OriginErrorsTotal != nil {
			scopedSinks.Gauge("cdn.delivery.origin", "error", *v.OriginErrorsTotal, tags)
		}
	}
	slog.Debug("Completed Akamai Errors polling", "rows", len(r.Data))
}

func (c *Client) PollAkamaiQuantiles() {
	// Check if context is cancelled before starting work
	select {
	case <-c.ctx.Done():
		slog.Debug("Skipping Akamai Quantiles poll - context cancelled")
		return
	default:
	}

	pollTags := sinks.Tags{"query": "quantiles"}
	stopTimer := sinks.StartTimer(c.selfSink, "hydrolix.collector.poll.duration", pollTags)
	defer stopTimer()

	slog.Debug("Polling Akamai Quantiles data")
	var r AkamaiEdgeQuantilesResponse
	if err := c.QueryHydrolix(&r, FormattedSQLAkamaiQuantiles); err != nil {
		slog.Error("Failed to query Akamai quantiles", "error", err)
		c.selfSink.Inc("hydrolix.collector.poll", "total", 1, sinks.MergeTags(pollTags, sinks.Tags{"status": "error"}))
		return
	}
	c.selfSink.Inc("hydrolix.collector.poll", "total", 1, sinks.MergeTags(pollTags, sinks.Tags{"status": "success"}))
	c.selfSink.Gauge("hydrolix.collector.poll.rows", "row", float64(len(r.Data)), pollTags)

	percentiles := []string{"p25", "p50", "p75", "p90", "p95", "p99"}

	for _, v := range r.Data {
		// Check if context is cancelled during processing
		select {
		case <-c.ctx.Done():
			slog.Debug("Context cancelled during Akamai Quantiles processing")
			return
		default:
		}

		// Skip rows with nil required fields
		if v.TimeMinute == nil || v.Hostname == nil || v.StatusClass == nil {
			slog.Warn("Skipping quantile row with nil required fields")
			continue
		}

		// Base tags that don't change per metric
		baseTags := make(map[string]string, 5)
		baseTags["cdn"] = "akamai"
		baseTags["source"] = "hydrolix"
		baseTags["hostname"] = *v.Hostname
		baseTags["status_class"] = *v.StatusClass
		if v.CacheStatus != nil {
			baseTags["cache_status"] = fmt.Sprintf("%d", *v.CacheStatus)
		}

		ts := convertTime(*v.TimeMinute)
		scopedSinks := c.sinks.WithTimestamp(ts)

		// Helper to send quantile metrics
		sendQuantiles := func(values []float64, metricName, unit string) {
			for idx, val := range values {
				if idx >= len(percentiles) {
					break
				}
				tags := make(map[string]string, len(baseTags)+1)
				for k, v := range baseTags {
					tags[k] = v
				}
				tags["percentile"] = percentiles[idx]
				scopedSinks.Gauge(metricName, unit, val, tags)
			}
		}

		sendQuantiles(v.FirstByteSecs, "cdn.performance.edge.firstbyte", "second")
		sendQuantiles(v.TLSOverheadSecs, "cdn.performance.edge.tls", "second")
		sendQuantiles(v.TurnaroundSecs, "cdn.performance.edge.turnaround", "second")
		sendQuantiles(v.DownloadSecs, "cdn.performance.edge.download", "second")
		sendQuantiles(v.OverheadBytes, "cdn.performance.edge.overhead", "byte")
		sendQuantiles(v.TransferSecs, "cdn.performance.edge.transfer", "second")
	}
	slog.Debug("Completed Akamai Quantiles polling", "rows", len(r.Data))
}

func convertTime(ts string) int64 {
	// ClickHouse timestamp_min format: "2006-01-02 15:04:05"
	t, err := time.ParseInLocation("2006-01-02 15:04:05", ts, time.UTC)
	if err != nil {
		slog.Error("Failed to parse timestamp, using current time", "value", ts, "error", err)
		return time.Now().Unix()
	}
	return t.Unix()
}

func (c *Client) UpdateToken(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	login := loginRequest{c.opts.Username, c.opts.Password}
	data, err := json.Marshal(login)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	url := fmt.Sprintf("https://%s/config/v1/login/", c.opts.Host)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("login failed: status=%d body=%s", resp.StatusCode, string(body))
	}

	var lr loginResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		return fmt.Errorf("unmarshal login response: %w", err)
	}
	if lr.AccessToken == "" {
		return fmt.Errorf("login response missing access_token")
	}

	c.token = lr.AccessToken
	return nil
}

// ---------- Builder ----------

func (c *Client) Query(sql string) (string, error) {
	url := fmt.Sprintf("https://%s/query/", c.opts.Host)

	// Use context for cancellation support
	req, err := http.NewRequestWithContext(c.ctx, http.MethodPost, url, strings.NewReader(sql))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}

	c.mu.RLock()
	token := c.token
	c.mu.RUnlock()

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "text/plain")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("query failed: status=%d body=%s", resp.StatusCode, string(body))
	}

	return string(body), nil
}
