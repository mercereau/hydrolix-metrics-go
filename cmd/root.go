package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mercereau/hydrolix-metrics-go/internal/common"
	"github.com/mercereau/hydrolix-metrics-go/internal/logger"
	"github.com/mercereau/hydrolix-metrics-go/internal/poller/hydrolix"
	"github.com/mercereau/hydrolix-metrics-go/internal/sinks"
	"github.com/mercereau/hydrolix-metrics-go/internal/sinks/datadog"
	"github.com/mercereau/hydrolix-metrics-go/internal/sinks/otel"
	"github.com/mercereau/hydrolix-metrics-go/internal/sinks/prometheus"
)

var (
	verbose bool

	rootCmd = &cobra.Command{
		Use:   "hydrolix-collector",
		Short: "CLI for collecting CDN Metrics from Hydrolix",
		Long:  "CLI for collecting CDN Metrics from Hydrolix and exporting them to various metric sinks.",
		RunE: func(cmd *cobra.Command, args []string) error {
			slog.Info("Running hydrolix-collector...")
			RunHydrolixCollector()
			return nil
		},
	}
)

var (
	namespace     string
	subsystem     string
	environment   string
	polling       int32
	flush         int32
	ddQueueSize   int
	ddBatchSize   int
	ddInitBackoff int
	ddMaxRetries  int
	concurrency        uint16
	offsetStartMinutes int
	offsetEndMinutes   int
	promPort           string
	promPath      string
	otelEndpoint  string
	sinx          []string
	healthz       string

	metricsinks []sinks.MetricSink
)

func init() {
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable verbose output")

	rootCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "", "override metric namespace, default uses command")
	rootCmd.PersistentFlags().StringVarP(&subsystem, "subsystem", "s", "exporter", "provide a subsystem for the namespace")
	rootCmd.PersistentFlags().StringVarP(&environment, "environment", "e", "dev", "environment that the exporter runs in")
	rootCmd.PersistentFlags().Int32VarP(&polling, "interval", "i", 15, "polling interval in seconds for the exporter, how frequently the exporter polls")
	rootCmd.PersistentFlags().Int32VarP(&flush, "flush", "f", 15, "how frequently the metric sinks should export metrics in seconds")
	rootCmd.PersistentFlags().Uint16VarP(&concurrency, "concurrency", "C", 1, "concurrency level for the exporter, how many simultaneous requests to make")
	rootCmd.PersistentFlags().IntVar(&offsetStartMinutes, "offset-start-minutes", 6, "how many minutes back the query window starts")
	rootCmd.PersistentFlags().IntVar(&offsetEndMinutes, "offset-end-minutes", 1, "lag offset in minutes for the query window end")

	rootCmd.PersistentFlags().StringSliceVarP(&sinx, "sink", "S", nil, "metrics sinks to enable, options: datadog, prometheus, otel")

	rootCmd.PersistentFlags().IntVarP(&ddBatchSize, "sink-datadog-batch-size", "b", 200, "[DataDog Sink] size of batch of custom metrics for a payload")
	rootCmd.PersistentFlags().IntVarP(&ddQueueSize, "sink-datadog-queue-size", "q", 25, "[DataDog Sink] size of payload queue for custom metrics")
	rootCmd.PersistentFlags().IntVarP(&ddInitBackoff, "sink-datadog-backoff", "B", 1, "[DataDog Sink] initial backoff time for retries")
	rootCmd.PersistentFlags().IntVarP(&ddMaxRetries, "sink-datadog-max-retries", "M", 3, "[DataDog Sink] max retries before failure to send custom metrics")

	rootCmd.PersistentFlags().StringVarP(&promPort, "sink-prometheus-port", "p", ":2112", "[Prometheus Sink] port for prometheus endpoint")
	rootCmd.PersistentFlags().StringVarP(&promPath, "sink-prometheus-path", "m", "/metrics", "[Prometheus Sink] path for endpoint")

	rootCmd.PersistentFlags().StringVarP(&otelEndpoint, "sink-otel-endpoint", "o", "", "[OpenTelemetry Sink] comma-separated list of host:port for endpoint, e.g., localhost:4317")

	// add a healthcheck persisntent flag
	rootCmd.PersistentFlags().StringVarP(&healthz, "healthz", "", "/healthz", "healthcheck endpoint path")

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		slog.SetDefault(logger.New(verbose))
		namespace = strings.TrimSpace("hydrolix")
		normalizeSinks()
		createMetricSinks()
		return nil
	}
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func normalizeSinks() {
	for i := range sinx {
		sinx[i] = strings.ToLower(strings.TrimSpace(sinx[i]))
	}
	// optional dedupe
	out := make([]string, 0, len(sinx))
	seen := map[string]struct{}{}
	for _, v := range sinx {
		if _, ok := seen[v]; !ok && v != "" {
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	sinx = out
}

func createMetricSinks() {
	// Prometheus is created first so it can serve as SelfSink for other sinks.
	// Self-metrics from DataDog and OTel (drops, retries, evictions) flow to
	// Prometheus rather than back through their own channels.
	var selfSink sinks.MetricSink
	if slices.Contains(sinx, "prometheus") && promPort != "" && promPath != "" {
		ps := prometheus.NewSink(prometheus.PromOpts{
			Namespace:            namespace,
			Subsystem:            subsystem,
			RegisterGoCollectors: true,
			ListenAddr:           promPort,
			MetricsPath:          promPath,
		})
		selfSink = ps
		metricsinks = append(metricsinks, ps)
		slog.Info("configured prometheus sink", "addr", promPort, "path", promPath)
	}

	if slices.Contains(sinx, "datadog") && ddBatchSize != 0 {
		ds := datadog.NewSink(datadog.DatadogOpts{
			Namespace:      namespace,
			Subsystem:      subsystem,
			BaseTags:       map[string]string{"environment": environment},
			BatchSize:      ddBatchSize,
			QueueSize:      ddQueueSize,
			FlushInterval:  time.Duration(flush) * time.Second,
			InitialBackoff: time.Duration(ddInitBackoff) * time.Second,
			MaxRetries:     ddMaxRetries,
			Concurrency:    concurrency,
			SelfSink:       selfSink,
		})
		metricsinks = append(metricsinks, ds)
		slog.Info("configured datadog sink",
			"batch_size", ddBatchSize,
			"queue_size", ddQueueSize,
			"flush_interval", time.Duration(flush)*time.Second,
			"max_retries", ddMaxRetries,
		)
	}

	if slices.Contains(sinx, "otel") && otelEndpoint != "" {
		for endpoint := range strings.SplitSeq(otelEndpoint, ",") {
			endpoint = strings.TrimSpace(endpoint)
			osink := otel.NewOTelSink(otel.OTelOpts{
				Endpoint:       endpoint,
				Protocol:       "grpc",
				Insecure:       true,
				ExportInterval: time.Duration(flush) * time.Second,
				ServiceName:    fmt.Sprintf("%s.%s", namespace, subsystem),
				ServiceVersion: "1.0.0",
				DeploymentEnv:  environment,
				SelfSink:       selfSink,
			})
			metricsinks = append(metricsinks, osink)
			slog.Info("configured otel sink", "endpoint", endpoint, "export_interval", time.Duration(flush)*time.Second)
		}
	}

	if len(metricsinks) == 0 {
		slog.Warn("no sinks configured; metrics will be discarded — use --sink to enable one or more sinks")
	} else {
		slog.Info("sink configuration complete", "count", len(metricsinks))
	}
}

func RunHydrolixCollector() {
	slog.Info("Starting Hydrolix Poller...")
	// Configure Hydrolix client
	hops := hydrolix.HydrolixOpts{
		IntervalSeconds:    time.Duration(polling) * time.Second,
		OffsetStartMinutes: offsetStartMinutes,
		OffsetEndMinutes:   offsetEndMinutes,
	}

	// Create Hydrolix client with Sinks
	c := hydrolix.New("Hydrolix Client", hops, metricsinks...)
	if c == nil {
		slog.Error("Failed to create Hydrolix client - check authentication configuration")
		return
	}

	// Start polling in background
	go c.Start()

	// Keep the application running
	stopCh := make(chan struct{})
	go common.WaitForKillSignal(stopCh)
	<-stopCh

	// Cleanup with timeout
	slog.Info("Shutting down Hydrolix Poller...")
	c.Stop()
	slog.Info("Hydrolix Poller stopped")
}
