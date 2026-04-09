package prometheus

import (
	"io/ioutil"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mercereau/hydrolix-metrics-go/internal/sinks"
)

func TestSanitizeAndNames(t *testing.T) {
	s := NewSink(PromOpts{Namespace: "MyNS", Subsystem: "SubS"})
	p := s.p
	// fqName
	got := p.fqName("Metric.Name")
	if !strings.HasPrefix(got, "myns_subs_") {
		t.Fatalf("unexpected fqName: %s", got)
	}

	// counterName adds _total
	cn := p.counterName("requests", "")
	if !strings.HasSuffix(cn, "_total") {
		t.Fatalf("counter name missing _total: %s", cn)
	}
	// gaugeName doesn't add _total
	gn := p.gaugeName("requests", "ms")
	if !strings.Contains(gn, "_ms") {
		t.Fatalf("gauge name missing unit suffix: %s", gn)
	}
}

func TestLabelsForAndMergeTags(t *testing.T) {
	s := NewSink(PromOpts{})
	p := s.p
	tags1 := sinks.Tags{"a": "1", "b": "2"}
	lbls := p.labelsFor("m1", tags1)
	if len(lbls) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(lbls))
	}
	// call again with extra key; must return same label set
	tags2 := sinks.Tags{"a": "1", "b": "2", "c": "3"}
	lbls2 := p.labelsFor("m1", tags2)
	if len(lbls2) != len(lbls) {
		t.Fatalf("labels changed between calls: %v vs %v", lbls, lbls2)
	}

	// mergeTags sanitizes keys
	merged := mergeTags(sinks.Tags{"9k": "v"}, sinks.Tags{"X": "y"})
	for k := range merged {
		if strings.HasPrefix(k, "9") {
			t.Fatalf("sanitizeLabelKey did not prefix numeric key: %s", k)
		}
	}
}

func TestStartStopHTTPServer(t *testing.T) {
	opts := PromOpts{ListenAddr: "127.0.0.1:0", MetricsPath: "/metrics_test", ShutdownTimeout: 2 * time.Second}
	s := NewSink(opts)
	// create some metrics
	s.Gauge("g1", "", 3.2, sinks.Tags{"k": "v"})

	s.Start()
	// ensure listener exists
	if s.p.ln == nil {
		t.Fatalf("expected listener to be created")
	}
	addr := s.p.ln.Addr().String()
	// request metrics path
	res, err := http.Get("http://" + addr + opts.MetricsPath)
	if err != nil {
		t.Fatalf("http get failed: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := ioutil.ReadAll(res.Body)
		t.Fatalf("unexpected status %d body=%s", res.StatusCode, string(b))
	}

	s.Stop()
}
