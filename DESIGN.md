# Design Decisions

This document explains the reasoning behind the key design choices in `hydrolix-metrics-go`.

## Why Convert Raw Logs to Time-Series Metrics

Raw logs are invaluable for troubleshooting and triage -- when you know what you're looking for, there's no substitute for the full detail of an individual request. But raw logs and time-series metrics serve fundamentally different purposes. This relationship maps to the DIKW (Data, Information, Knowledge, Wisdom) hierarchy:

```
             /\
            /  \
           /    \      Wisdom: "What should we do?"
          /Wisdom\     Decisions, capacity planning, SLO targets
         /        \
        /----------\
       /            \    Knowledge: "Why is it happening"
      /  Knowledge   \    Correlations, trends, anomaly detection
     /                \
    /------------------\
   /                    \   Information: "How are we doing?"
  /     Information      \   Time-series metrics, dashboards, alerts
 /                        \
/--------------------------\
|                          |  Data: "What happened?"
|           Data           |  Individual events, request details
|                          |
\--------------------------/
```

Raw logs sit at the **Data** layer. This tool moves up the hierarchy by converting raw data into **Information** -- pre-aggregated time-series metrics that power dashboards and alerts. From there, teams derive **Knowledge** (trend analysis, correlations) and ultimately **Wisdom** (capacity decisions, SLO targets).

- **Query performance at scale**: Aggregating millions of log rows on every dashboard load becomes increasingly expensive as the time window grows. Time-series metrics are pre-aggregated, making queries over hours, days, or weeks return in milliseconds rather than minutes.
- **Signal from noise**: Logs capture everything. Metrics distill that into trends, rates, and distributions -- the patterns that tell you something has changed before you need to dig into individual records.
- **Alerting**: Monitoring and alerting systems are built around time-series data. Threshold alerts, anomaly detection, and SLO tracking all expect numeric series, not log lines.
- **Cost efficiency**: Storing and querying pre-aggregated metrics is orders of magnitude cheaper than scanning raw logs repeatedly. The logs remain available for deep investigation, but day-to-day visibility runs on the lightweight metric layer.
- **Retention and comparison**: Time-series data compresses well and retains cheaply over long periods, making week-over-week or month-over-month comparisons practical without expensive log re-aggregation.

In short, raw logs answer "what happened?" while time-series metrics answer "how are we doing?" This tool bridges the two -- it reads the raw data in Hydrolix and produces the metric signal that powers dashboards, alerts, and capacity planning.

## Why Go

Go was chosen for its ability to compile to a single binary that can be built and deployed anywhere, its first-class concurrency primitives, and its performance characteristics for an I/O-bound polling service.

## Sink Abstraction

The `MetricSink` interface allows multiple sinks (Prometheus, Datadog, OpenTelemetry, StatsD) to run simultaneously, receiving identical time-series data. This serves the "single pane of glass" problem in reverse: not everyone has access to the same observability platform. By emitting to multiple sinks at once, no matter which system you're looking through, you have visibility to the same view.

## Cobra CLI

Cobra is a well-known Go CLI framework that allows the service to be extended beyond its current capabilities with subcommands, flags, and built-in help generation.

## Polling Model and Query Window

The collector uses a configurable polling model with a default 5-minute query window (6 minutes back, 1 minute lag):

- **5-minute window**: Allows for data backfill in metric sinks that support backfilled time-series. It also accommodates typical vendor SLAs for real-time log streaming to obtain 100% of raw log data and populate summary tables.
- **1-minute end offset**: Accounts for vendor SLAs of 95% of logs delivered within 30 seconds of log creation, plus a 30-second buffer for data indexing and ETL jobs to generate data in summary tables.

## Parallel Queries

Each poll function runs a distinct SQL query to retrieve a specific data shape. All poll functions execute concurrently via a `sync.WaitGroup` fan-out, so additional queries can be added without increasing tick latency.

## Prometheus as Self-Sink

Internal metrics (drops, retries, poll durations) are reported through Prometheus rather than through the sink generating them. Prometheus was chosen as the self-metrics bus because exporters already exist to bridge Prometheus data to Datadog, OpenTelemetry, and other systems.

## Channel-Based Backpressure

The Datadog sink drops metrics when its buffer is full rather than blocking the producer. This protects the polling loop from stalling. Dropping is acceptable because the 5-minute backfill window means the data will be picked up on the next poll cycle. The sink also uses exponential backoff on retries -- if Datadog is not accepting POSTs now, the next successful cycle will backfill the gap.

## Docker Image

Multi-stage build with a static binary (`CGO_ENABLED=0`) on Alpine. This is a pragmatic default -- no specific benchmarking was done against alternatives like scratch or distroless.

## Logging

Uses the standard library's `log/slog` for structured logging. No third-party logger needed.

## Credentials via Environment Variables

Hydrolix credentials are read from environment variables (`HDX_TOKEN`, `HDX_HOST`, etc.) to align with internal tooling conventions. Tokens can also be passed via CLI flags for flexibility.

## Single Instance, Not a Cluster

The collector runs as a single instance by design. It fires 3 SQL queries every 15 seconds -- Hydrolix does the heavy aggregation, so the collector itself is lightweight with nothing compute-intensive to distribute.

Running multiple instances without coordination causes problems: duplicate queries hit Hydrolix unnecessarily, Datadog and OTel receive double-counted metrics, and Prometheus gets confused by multiple scrape targets exposing identical series.

The 5-minute backfill window provides natural resilience. If the instance restarts, the next tick re-queries the same window and fills the gap. For any outage under 5 minutes, no data is lost. A single-replica Kubernetes Deployment with health checks and a restart policy is sufficient -- the backfill window makes restarts self-healing.

If zero-gap metric continuity becomes a requirement (e.g., metrics drive paging alerts with tight SLOs), active/standby with a Kubernetes Lease for leader election would be the lightest-weight option. This has not been built because the need is not yet concrete, and it adds real operational complexity (coordination infrastructure, split-brain edge cases) for a service that recovers naturally in under a minute.

## ldflags Versioning

Version, commit, and date are injected at build time via ldflags rather than using Go's `debug.BuildInfo`. This allows git branch information to be embedded directly into the release version string.
