package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrometheusEndpointAndCardinality(t *testing.T) {
	m := New()
	m.SetBackend("http://backend:8080", true)
	m.Dropped.Inc()
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }))
	for _, path := range []string{"/users/123", "/users/456"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
	}
	res := httptest.NewRecorder()
	m.Handler().ServeHTTP(res, httptest.NewRequest("GET", "/metrics", nil))
	for _, line := range []string{
		`http_requests_total{method="GET",path="proxy",status_code="429"} 2`,
		`backend_status{backend_url="http://backend:8080"} 1`,
		`rate_limit_dropped_total 1`,
		`http_request_duration_seconds_count{method="GET",path="proxy"} 2`,
	} {
		if !strings.Contains(res.Body.String(), line) {
			t.Errorf("missing metric: %s", line)
		}
	}
	if strings.Contains(res.Body.String(), "/users/") {
		t.Fatal("raw paths leaked into metric labels")
	}
}
