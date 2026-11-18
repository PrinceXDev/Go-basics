// Package metrics defines every Prometheus metric in one place.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	Reg *prometheus.Registry

	HTTPDuration *prometheus.HistogramVec // route, method, status
	Produced     *prometheus.CounterVec   // result=ok|error
	Consumed     *prometheus.CounterVec   // topic
	Applied      prometheus.Counter       // events that changed aggregates
	Duplicates   prometheus.Counter       // events dropped by dedup
	Failed       *prometheus.CounterVec   // reason=invalid|retry|dlq|db
	E2ELatency   prometheus.Histogram     // clicked_at -> aggregated, seconds
	BatchSize    prometheus.Histogram
	DBDuration   *prometheus.HistogramVec // op
	ConsumerLag  *prometheus.GaugeVec     // topic, partition
	Rebalances   prometheus.Counter
}

func New() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	f := func(c prometheus.Collector) { reg.MustRegister(c) }

	m := &Metrics{
		Reg: reg,
		HTTPDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "adclick_http_request_duration_seconds", Help: "API request latency.",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5}}, []string{"route", "method", "status"}),
		Produced: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "adclick_events_produced_total", Help: "Click events handed to Kafka."}, []string{"result"}),
		Consumed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "adclick_events_consumed_total", Help: "Records read from Kafka."}, []string{"topic"}),
		Applied: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "adclick_events_applied_total", Help: "Events that changed aggregates."}),
		Duplicates: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "adclick_events_duplicate_total", Help: "Events skipped because event_id was already processed."}),
		Failed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "adclick_events_failed_total", Help: "Failed events by outcome."}, []string{"reason"}),
		E2ELatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "adclick_event_processing_latency_seconds", Help: "Time from click to aggregate commit.",
			Buckets: []float64{.05, .1, .25, .5, 1, 2, 5, 10, 30, 60, 300}}),
		BatchSize: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "adclick_consumer_batch_size", Help: "Records per processed batch.",
			Buckets: []float64{1, 10, 50, 100, 250, 500, 1000}}),
		DBDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "adclick_db_query_duration_seconds", Help: "Database operation latency.",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 5}}, []string{"op"}),
		ConsumerLag: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "adclick_consumer_lag", Help: "Records not yet consumed per partition."}, []string{"topic", "partition"}),
		Rebalances: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "adclick_consumer_rebalances_total", Help: "Partition revocations seen."}),
	}
	for _, c := range []prometheus.Collector{m.HTTPDuration, m.Produced, m.Consumed, m.Applied, m.Duplicates,
		m.Failed, m.E2ELatency, m.BatchSize, m.DBDuration, m.ConsumerLag, m.Rebalances} {
		f(c)
	}
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Reg, promhttp.HandlerOpts{})
}

// ObserveDB matches the store's observe hook.
func (m *Metrics) ObserveDB(op string, d time.Duration) {
	m.DBDuration.WithLabelValues(op).Observe(d.Seconds())
}
