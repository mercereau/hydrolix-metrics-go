package hydrolix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mercereau/hydrolix-metrics-go/internal/common"
	"github.com/mercereau/hydrolix-metrics-go/internal/sinks"
)

type Client struct {
	name       string
	token      string
	sinks      sinks.MetricSinks
	selfSink   sinks.MetricSink // scoped sink for collector self-metrics
	opts       HydrolixOpts
	config     *QueriesConfig
	httpClient *http.Client

	mu      sync.RWMutex
	closeCh chan struct{}
	closed  bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

type HydrolixOpts struct {
	Host               string
	Username           string
	Password           string
	Token              string
	IntervalSeconds    time.Duration // Polling interval duration (e.g., 15 * time.Second)
	OffsetStartMinutes int           // How far back the query window starts (default 6)
	OffsetEndMinutes   int           // Lag offset for the query window end (default 1)
	ConfigPath         string        // Path to external queries.yaml (empty = use embedded defaults)
	EmbeddedFS         fs.FS         // Embedded configs filesystem from project root
}

type loginRequest struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}
type loginResponse struct {
	AccessToken string `json:"access_token"`
}

func New(name string, o HydrolixOpts, ms ...sinks.MetricSink) *Client {
	// Load query config.
	cfg, err := LoadConfig(o.ConfigPath, o.EmbeddedFS)
	if err != nil {
		slog.Error("Failed to load query config", "error", err)
		return nil
	}

	// CLI offset flags override config defaults when non-zero.
	if o.OffsetStartMinutes != 0 {
		cfg.Defaults.OffsetStartMinutes = o.OffsetStartMinutes
		// Re-resolve queries with updated offsets.
		cfg, err = reResolveOffsets(cfg)
		if err != nil {
			slog.Error("Failed to re-resolve config with CLI offsets", "error", err)
			return nil
		}
	}
	if o.OffsetEndMinutes != 0 {
		cfg.Defaults.OffsetEndMinutes = o.OffsetEndMinutes
		cfg, err = reResolveOffsets(cfg)
		if err != nil {
			slog.Error("Failed to re-resolve config with CLI offsets", "error", err)
			return nil
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		name:       name,
		opts:       o,
		config:     cfg,
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

// reResolveOffsets re-renders SQL templates for queries that inherit default offsets.
func reResolveOffsets(cfg *QueriesConfig) (*QueriesConfig, error) {
	for i := range cfg.Queries {
		q := &cfg.Queries[i]
		// Only update queries that inherit from defaults (no per-query override).
		if q.OffsetStartMinutes == nil {
			q.resolvedOffsetStart = cfg.Defaults.OffsetStartMinutes
		}
		if q.OffsetEndMinutes == nil {
			q.resolvedOffsetEnd = cfg.Defaults.OffsetEndMinutes
		}
		// Re-render SQL template.
		if err := renderQuerySQL(q); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

func (c *Client) refreshToken() {
	defer c.wg.Done()

	refreshInterval := 1 * time.Hour
	if c.opts.IntervalSeconds > 0 && c.opts.IntervalSeconds < refreshInterval {
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
	c.wg.Add(1)
	defer c.wg.Done()

	for _, m := range c.sinks {
		m.Start()
	}

	pollingInterval := c.opts.IntervalSeconds
	if pollingInterval <= 0 {
		pollingInterval = 15 * time.Second
		slog.Warn("Invalid polling interval, using default", "default", pollingInterval)
	}

	ticker := time.NewTicker(pollingInterval)
	defer ticker.Stop()

	slog.Info("Starting Hydrolix poller", "interval", pollingInterval, "queries", len(c.config.Queries))

	// Poll immediately on start.
	c.pollAll()

	for {
		select {
		case <-c.ctx.Done():
			slog.Info("Hydrolix poller stopping")
			return
		case <-ticker.C:
			c.pollAll()
		}
	}
}

// pollAll fans out all configured queries concurrently.
func (c *Client) pollAll() {
	var wg sync.WaitGroup
	wg.Add(len(c.config.Queries))

	for i := range c.config.Queries {
		go func(q *QueryConfig) {
			defer wg.Done()
			c.pollQuery(q)
		}(&c.config.Queries[i])
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-c.ctx.Done():
		slog.Debug("Context cancelled while waiting for polls to complete")
	}
}

// pollQuery executes a single configured query and emits metrics.
func (c *Client) pollQuery(q *QueryConfig) {
	select {
	case <-c.ctx.Done():
		slog.Debug("Skipping poll - context cancelled", "query", q.Name)
		return
	default:
	}

	pollTags := sinks.Tags{"query": q.Name}
	stopTimer := sinks.StartTimer(c.selfSink, "hydrolix.collector.poll.duration", pollTags)
	defer stopTimer()

	slog.Debug("Polling query", "name", q.Name)

	body, err := c.Query(q.RenderedSQL())
	if err != nil {
		slog.Error("Failed to query Hydrolix", "query", q.Name, "error", err)
		c.selfSink.Inc("hydrolix.collector.poll", "total", 1, sinks.MergeTags(pollTags, sinks.Tags{"status": "error"}))
		return
	}

	var resp GenericResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		slog.Error("Failed to unmarshal response", "query", q.Name, "error", err)
		c.selfSink.Inc("hydrolix.collector.poll", "total", 1, sinks.MergeTags(pollTags, sinks.Tags{"status": "error"}))
		return
	}

	c.selfSink.Inc("hydrolix.collector.poll", "total", 1, sinks.MergeTags(pollTags, sinks.Tags{"status": "success"}))
	c.selfSink.Gauge("hydrolix.collector.poll.rows", "row", float64(len(resp.Data)), pollTags)

	emitMetrics(q, &resp, c.config.Defaults.Tags, c.sinks)

	slog.Debug("Completed polling", "query", q.Name, "rows", len(resp.Data))
}

// Stop signals all listeners that we're shutting down.
func (c *Client) Stop() {
	slog.Info("Stopping Hydrolix client")
	c.mu.Lock()

	if c.closed {
		c.mu.Unlock()
		slog.Warn("Hydrolix client already stopped")
		return
	}
	c.closed = true
	c.mu.Unlock()

	c.cancel()

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

	close(c.closeCh)

	for _, m := range c.sinks {
		m.Stop()
		slog.Debug("Stopped sink")
	}
	slog.Info("Hydrolix client stopped")
}

func (c *Client) done() <-chan struct{} {
	return c.closeCh
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

func (c *Client) Query(sql string) (string, error) {
	url := fmt.Sprintf("https://%s/query/", c.opts.Host)

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
