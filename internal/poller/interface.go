package poller

import (
	"github.com/mercereau/hydrolix-metrics-go/internal/sinks"
)

type Poller interface {
	Register(sinks.MetricSink)
	Start()
	Stop()
}
