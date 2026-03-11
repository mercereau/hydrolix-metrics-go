package collector

type MetricSink interface {
	Start()
	Stop()

	Metrics() []MetricSink
}
