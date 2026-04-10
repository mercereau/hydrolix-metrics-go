# Architecture

## High-Level Overview

The collector polls Hydrolix for log data, transforms it into pre-aggregated time-series metrics, and fans them out to one or more metric sinks (Datadog, Prometheus, OpenTelemetry).

```mermaid
flowchart TB
    subgraph External
        HDX[(Hydrolix<br/>SQL-over-HTTP)]
        DD[Datadog API]
        PROM_SC[Prometheus Scraper]
        OTEL_COL[OTel Collector]
    end

    subgraph CLI["Cobra CLI (cmd/root.go)"]
        INIT[Initialize Logger + Sinks]
        RUN[RunHydrolixCollector]
        SIG[Signal Handler<br/>SIGINT / SIGTERM]
    end

    subgraph Poller["Hydrolix Poller (internal/poller/hydrolix)"]
        TICKER[Ticker Loop<br/>default 15s]
        Q1[PollAkamaiEdgeRequests]
        Q2[PollAkamaiQuantiles]
        Q3[PollAkamaiErrors]
        TOKEN[Token Refresh<br/>goroutine]
    end

    subgraph Sinks["Metric Sinks (internal/sinks)"]
        IF{MetricSink<br/>Interface}
        DS[Datadog Sink]
        PS[Prometheus Sink]
        OS[OTel Sink]
        NOP[Nop Sink]
    end

    INIT --> RUN
    RUN -->|"go c.Start()"| TICKER
    SIG -->|"c.Stop()"| TICKER

    TICKER -->|"every tick"| Q1 & Q2 & Q3
    Q1 & Q2 & Q3 -->|SQL POST| HDX
    Q1 & Q2 & Q3 -->|"WithTimestamp().Gauge/Inc"| IF

    TOKEN -->|"hourly"| HDX

    IF --> DS & PS & OS
    DS -->|"HTTP POST"| DD
    PS -->|"HTTP /metrics"| PROM_SC
    OS -->|"gRPC OTLP"| OTEL_COL
```

## Concurrency Model

Every goroutine in the system is tracked by a `sync.WaitGroup` and cancellable via `context.WithCancel`, enabling graceful shutdown with a 10-second timeout.

```mermaid
flowchart LR
    subgraph main["main goroutine"]
        direction TB
        M1["Execute()"] --> M2["go c.Start()"]
        M2 --> M3["WaitForKillSignal()"]
        M3 -->|SIGINT/SIGTERM| M4["c.Stop() → cancel ctx"]
    end

    subgraph poller["Poller goroutines"]
        direction TB
        G1["goroutine: Start()<br/>Ticker loop"]
        G2["goroutine: refreshToken()<br/>1-hour ticker"]

        G1 -->|"every tick"| FAN

        subgraph FAN["Fan-out per tick (WaitGroup)"]
            direction LR
            P1["goroutine: PollEdgeRequests"]
            P2["goroutine: PollQuantiles"]
            P3["goroutine: PollErrors"]
        end
    end

    subgraph ddsink["Datadog Sink goroutines"]
        direction TB
        C1["goroutine: runCollector #1"]
        CN["goroutine: runCollector #N"]
        S1["goroutine: runSender"]
    end

    subgraph promsink["Prometheus Sink"]
        HTTP["HTTP Server goroutine<br/>(pull-based, no workers)"]
    end

    subgraph otelsink["OTel Sink"]
        PR["PeriodicReader goroutine<br/>(flush interval)"]
        CL["goroutine: cleanupGaugePrev<br/>(every 5 min)"]
    end

    M4 -.->|"ctx.Done()"| G1 & G2 & P1 & P2 & P3
    M4 -.->|"sink.Stop()"| C1 & CN & S1 & HTTP & PR & CL
```

### Concurrency Summary

| Component | Pattern | Goroutines | Coordination |
|---|---|---|---|
| Poller main loop | Ticker | 1 | `context.WithCancel` |
| Token refresh | Ticker | 1 | `context.WithCancel`, `sync.RWMutex` on token |
| Per-tick queries | Fan-out / fan-in | 3 (per tick) | `sync.WaitGroup` |
| Datadog collectors | Worker pool | N (configurable) | Channels, `sync.WaitGroup` |
| Datadog sender | Single consumer | 1 | Channel |
| Prometheus | HTTP server | 1 | `sync.Mutex` on registry |
| OTel | Periodic reader + cleanup | 2 | `sync.RWMutex` on gauge state |

### Query Window

Each tick fires three parallel SQL queries against a sliding 5-minute window offset from "now" to account for log ingestion lag:

```
  wall clock
──────────────────────────────────────────────►
                 ◄── 6 min ──►◄─ 1 min lag ─►
                 │            │              │
           offsetStart    offsetEnd         now

  Query window: [now - 6min, now - 1min]
```

The 1-minute lag accommodates log delivery SLA (95% within 30s) plus Hydrolix indexing time.

## Datadog Sink Pipeline

The Datadog sink uses a **3-stage buffered pipeline** with backpressure and retry logic. Metrics flow through channels from producers to a batching collector to a serializing sender.

```mermaid
flowchart TB
    subgraph Producers["Metric Producers (poller goroutines)"]
        P1["PollEdgeRequests"]
        P2["PollQuantiles"]
        P3["PollErrors"]
    end

    subgraph Stage1["Stage 1: Submit"]
        SUB["submitMetric()<br/>non-blocking select"]
        DROP["Drop metric +<br/>increment<br/>metrics_dropped<br/>(self-sink)"]
    end

    subgraph Stage2["Stage 2: Collect & Batch"]
        direction TB
        MCH["metricCh<br/>(buffered channel)<br/>capacity: BatchSize * Concurrency * 2"]
        COL["runCollector() workers<br/>(default: 1, configurable)"]
        BUF["Buffer up to BatchSize<br/>Flush on: batch full OR timer fires"]
    end

    subgraph Stage3["Stage 3: Send"]
        PCH["payloadsCh<br/>(buffered channel)<br/>capacity: QueueSize"]
        SND["runSender()<br/>(single goroutine)"]
    end

    subgraph Retry["Retry Logic"]
        DD["Datadog API<br/>POST /api/v2/series"]
        BACK["Exponential Backoff<br/>delay = initialBackoff * 2^attempt * jitter<br/>up to MaxRetries"]
        FAIL["Increment<br/>send_failures<br/>(self-sink)"]
    end

    P1 & P2 & P3 -->|".Gauge() / .Inc()"| SUB
    SUB -->|"channel full"| DROP
    SUB -->|"send OK"| MCH
    MCH --> COL
    COL --> BUF
    BUF -->|"flush()"| PCH
    PCH --> SND
    SND --> DD
    DD -->|"HTTP error"| BACK
    BACK -->|"retries exhausted"| FAIL
    DD -->|"2xx"| OK["Increment<br/>payloads_sent<br/>(self-sink)"]
```

### Backpressure Behavior

The pipeline is **non-blocking by design** -- the poller never stalls waiting for Datadog:

1. **`submitMetric()`** uses a non-blocking `select` on `metricCh`. If the channel is full, the metric is dropped and a `metrics_dropped` counter is incremented on the self-sink (typically Prometheus).
2. **`flush()`** enqueues payloads to `payloadsCh` with a 5-second timeout. If the queue is full for 5s, the payload is lost and logged.
3. **`sendToDatadog()`** retries with exponential backoff (`2^attempt * jitter`). After `MaxRetries` failures, the payload is discarded and `send_failures` is incremented.

Because the poller refills the 5-minute query window every tick, a brief drop is self-healing -- the next poll re-aggregates the same time range.

### Scoped Views for Thread Safety

The Datadog sink uses an **immutable scoped view** pattern to avoid data races:

```mermaid
classDiagram
    class MetricSink {
        <<interface>>
        +Start()
        +Stop()
        +Gauge(name, unit, value, tags)
        +Inc(name, unit, value, tags)
        +Rate(name, unit, value, tags)
        +Timing(name, duration, tags)
        +WithTags(tags) MetricSink
        +WithTimestamp(ts) MetricSink
    }

    class Sink {
        -mu sync.RWMutex
        -metricCh chan
        -payloadsCh chan
        -closed bool
        +Start()
        +Stop()
        +WithTags() MetricSink
        +WithTimestamp() MetricSink
        -submitMetric()
        -runCollector()
        -runSender()
    }

    class datadogScoped {
        -root *Sink
        -timestamp int64
        -base Tags
        +Gauge()
        +Inc()
        +WithTags() MetricSink
        +WithTimestamp() MetricSink
    }

    MetricSink <|.. Sink
    MetricSink <|.. datadogScoped
    Sink --> datadogScoped : "WithTags() / WithTimestamp()\ncreates new immutable view"
    datadogScoped --> Sink : "submits via root.submitMetric()"
```

When multiple goroutines call `WithTimestamp()` or `WithTags()`, each gets its own immutable `datadogScoped` struct. All scoped views funnel metrics back to the root `Sink` through `submitMetric()`, which is the single serialization point into `metricCh`.

### Self-Monitoring

The Datadog sink reports its own health metrics to a **separate sink** (typically Prometheus) to avoid feedback loops:

| Self-Metric | Type | Meaning |
|---|---|---|
| `hydrolix.sink.metrics_dropped` | Counter | Metrics dropped due to full `metricCh` |
| `hydrolix.sink.send_retries` | Counter | Retry attempts to Datadog API |
| `hydrolix.sink.payloads_sent` | Counter | Successfully sent payloads |
| `hydrolix.sink.send_failures` | Counter | Payloads dropped after all retries exhausted |

### Shutdown Sequence

```mermaid
sequenceDiagram
    participant Main as main goroutine
    participant Poller as Hydrolix Poller
    participant Collectors as runCollector workers
    participant Sender as runSender
    participant DD as Datadog API

    Main->>Poller: c.Stop() → cancel context
    Poller->>Poller: ctx.Done() → exit ticker loop
    Poller->>Collectors: stopCollectorCh signal
    Collectors->>Collectors: flush remaining buffer
    Collectors->>Sender: final payloads → payloadsCh
    Collectors-->>Poller: wg.Done()
    Poller->>Sender: stopSenderCh signal
    Sender->>Sender: drain payloadsCh
    Sender->>DD: send remaining payloads
    Sender-->>Poller: wg.Done()
    Poller->>Main: wg.Wait() returns (or 10s timeout)
    Main->>Main: close channels, exit
```

The shutdown is **ordered**: collectors flush and exit first, ensuring all buffered metrics reach the sender's queue before the sender drains and exits.
