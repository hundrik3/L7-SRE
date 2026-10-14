// Package metrics exposes bounded-cardinality Prometheus instrumentation.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	Registry      *prometheus.Registry
	Requests      *prometheus.CounterVec
	Duration      *prometheus.HistogramVec
	BackendStatus *prometheus.GaugeVec
	Dropped       prometheus.Counter
}

func New() *Metrics {
	m := &Metrics{
		Registry:      prometheus.NewRegistry(),
		Requests:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "http_requests_total", Help: "Completed data-plane requests."}, []string{"path", "method", "status_code"}),
		Duration:      prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "http_request_duration_seconds", Help: "Data-plane request duration including upstream body delivery.", Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 20}}, []string{"path", "method"}),
		BackendStatus: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "backend_status", Help: "Backend availability: 1 healthy, 0 unhealthy."}, []string{"backend_url"}),
		Dropped:       prometheus.NewCounter(prometheus.CounterOpts{Name: "rate_limit_dropped_total", Help: "Requests rejected by per-client rate limiting."}),
	}
	m.Registry.MustRegister(m.Requests, m.Duration, m.BackendStatus, m.Dropped, prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	return m
}

func (m *Metrics) SetBackend(url string, alive bool) {
	v := 0.0
	if alive {
		v = 1
	}
	m.BackendStatus.WithLabelValues(url).Set(v)
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}

// Middleware labels arbitrary proxy URLs as "proxy" to prevent unbounded
// cardinality from IDs, query strings or attacker-controlled paths.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w}
		defer func() {
			status := rw.status
			if status == 0 {
				status = http.StatusOK
			}
			method := r.Method
			switch method {
			case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE":
			default:
				method = "OTHER"
			}
			m.Requests.WithLabelValues("proxy", method, strconv.Itoa(status)).Inc()
			m.Duration.WithLabelValues("proxy", method).Observe(time.Since(start).Seconds())
		}()
		next.ServeHTTP(rw, r)
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	if status >= 200 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *responseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}
func (w *responseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
