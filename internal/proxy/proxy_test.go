package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hundrik3/L7-SRE/internal/balancer"
	"github.com/hundrik3/L7-SRE/internal/limiter"
)

func testProxy(t *testing.T, urls []string, headerTimeout time.Duration) (http.Handler, []*balancer.Backend) {
	t.Helper()
	backends := make([]*balancer.Backend, len(urls))
	for i, u := range urls {
		b, err := balancer.NewBackend(u)
		if err != nil {
			t.Fatal(err)
		}
		b.SetAlive(true)
		backends[i] = b
	}
	lb, err := balancer.NewRoundRobin(backends)
	if err != nil {
		t.Fatal(err)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = headerTimeout
	t.Cleanup(tr.CloseIdleConnections)
	r, _ := limiter.NewIPResolver(nil)
	p, err := New(Config{Balancer: lb, Transport: tr, Resolver: r, MaxAttempts: 3, Timeout: time.Second, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return p, backends
}

func TestFailover(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		slow         bool
		want         int
		healthyCalls int64
	}{
		{"server_error", "GET", false, 200, 1},
		{"timeout", "GET", true, 200, 1},
		{"write_is_not_retried", "POST", false, 503, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.slow {
					select {
					case <-r.Context().Done():
						return
					case <-time.After(time.Second):
					}
				}
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer bad.Close()
			var calls atomic.Int64
			good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = w.Write([]byte("healthy")) }))
			defer good.Close()
			p, backends := testProxy(t, []string{bad.URL, good.URL}, 30*time.Millisecond)
			req := httptest.NewRequest(tc.method, "http://loadbalancer/item?q=1", nil)
			res := httptest.NewRecorder()
			p.ServeHTTP(res, req)
			if res.Code != tc.want || calls.Load() != tc.healthyCalls {
				t.Fatalf("status=%d calls=%d", res.Code, calls.Load())
			}
			if backends[0].Alive() {
				t.Fatal("failed backend remained healthy")
			}
			for _, b := range backends {
				if b.ActiveRequests() != 0 {
					t.Fatal("reservation leaked")
				}
			}
		})
	}
}

func TestNoHealthyBackend(t *testing.T) {
	p, backends := testProxy(t, []string{"http://127.0.0.1:1"}, time.Second)
	backends[0].SetAlive(false)
	r := httptest.NewRecorder()
	p.ServeHTTP(r, httptest.NewRequest("GET", "/", nil))
	if r.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d", r.Code)
	}
}

func TestPathAndForwardingHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/api/item?q=1" {
			t.Errorf("bad target URI: %s", r.URL.RequestURI())
		}
		if r.Header.Get("X-Forwarded-For") != "192.0.2.1" {
			t.Errorf("untrusted forwarding header propagated: %s", r.Header.Get("X-Forwarded-For"))
		}
		if r.Header.Get("Forwarded") != "" {
			t.Error("untrusted Forwarded header propagated")
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	p, _ := testProxy(t, []string{upstream.URL + "/api"}, time.Second)
	req := httptest.NewRequest("GET", "http://lb/item?q=1", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.1")
	req.Header.Set("Forwarded", "for=203.0.113.1")
	res := httptest.NewRecorder()
	p.ServeHTTP(res, req)
	if res.Code != 200 || res.Body.String() != "ok" {
		t.Fatalf("got %d %s", res.Code, res.Body.String())
	}
}

func TestLeaseHeldUntilBodyClosed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("response")) }))
	defer upstream.Close()
	b, _ := balancer.NewBackend(upstream.URL)
	b.SetAlive(true)
	lb, _ := balancer.NewRoundRobin([]*balancer.Backend{b})
	tr := http.DefaultTransport.(*http.Transport).Clone()
	defer tr.CloseIdleConnections()
	r := &retryTransport{cfg: Config{Balancer: lb, Transport: tr, MaxAttempts: 1}}
	req, _ := http.NewRequest("GET", "http://placeholder/", nil)
	res, err := r.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if b.ActiveRequests() != 1 {
		t.Fatal("released before body close")
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	_ = res.Body.Close()
	if b.ActiveRequests() != 0 {
		t.Fatal("reservation leaked")
	}
}

func TestReplayPolicy(t *testing.T) {
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		req, _ := http.NewRequest(method, "/", strings.NewReader("body"))
		if replayable(req) {
			t.Errorf("%s accepted for replay", method)
		}
	}
	req, _ := http.NewRequest("GET", "/", io.NopCloser(strings.NewReader("body")))
	if replayable(req) {
		t.Fatal("non-rewindable GET body accepted")
	}
}

func TestCallerCancellationDoesNotMarkBackendDead(t *testing.T) {
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer upstream.Close()
	p, backends := testProxy(t, []string{upstream.URL}, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { p.ServeHTTP(httptest.NewRecorder(), req); close(done) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upstream not reached")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("request did not cancel")
	}
	if !backends[0].Alive() || backends[0].ActiveRequests() != 0 {
		t.Fatal("cancellation corrupted backend state")
	}
}

func TestAttemptLimit(t *testing.T) {
	var calls [4]atomic.Int64
	urls := make([]string, 4)
	for i := range urls {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls[i].Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		t.Cleanup(srv.Close)
		urls[i] = srv.URL
	}
	p, backends := testProxy(t, urls, time.Second)
	res := httptest.NewRecorder()
	p.ServeHTTP(res, httptest.NewRequest("GET", "/", nil))
	if res.Code != 503 {
		t.Fatalf("got %d, want final backend 503", res.Code)
	}
	for i, b := range backends {
		want := int64(1)
		if i == 3 {
			want = 0
		}
		if calls[i].Load() != want || b.ActiveRequests() != 0 {
			t.Fatalf("peer %d: calls=%d active=%d", i, calls[i].Load(), b.ActiveRequests())
		}
	}
}

func TestOverallTimeoutMarksBackendDead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	b, _ := balancer.NewBackend(srv.URL)
	b.SetAlive(true)
	lb, _ := balancer.NewRoundRobin([]*balancer.Backend{b})
	tr := http.DefaultTransport.(*http.Transport).Clone()
	defer tr.CloseIdleConnections()
	resolver, _ := limiter.NewIPResolver(nil)
	p, err := New(Config{Balancer: lb, Transport: tr, MaxAttempts: 3, Timeout: 30 * time.Millisecond, Resolver: resolver, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	p.ServeHTTP(res, httptest.NewRequest("GET", "/", nil))
	if res.Code != 504 || b.Alive() || b.ActiveRequests() != 0 {
		t.Fatalf("status=%d alive=%v active=%d", res.Code, b.Alive(), b.ActiveRequests())
	}
}
