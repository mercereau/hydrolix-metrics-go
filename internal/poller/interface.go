package poller

import (
	"gitlab-master.nvidia.com/it/sre/cdn/observability/hydrolix-collector/internal/sinks"
)

type Poller interface {
	Register(sinks.MetricSink)
	Start()
	Stop()
}
