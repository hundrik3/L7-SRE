package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hundrik3/L7-SRE/internal/balancer"
)

func TestFailureAndRecovery(t *testing.T) {
	var status atomic.Int64
	status.Store(200)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("wrong probe path: %s", r.URL.Path)
		}
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	b, _ := balancer.NewBackend(srv.URL + "/api")
	c := &Checker{Backends: []*balancer.Backend{b}, Client: &http.Client{Timeout: time.Second}, Interval: time.Millisecond}
	for _, code := range []int64{200, 503, 200} {
		status.Store(code)
		c.Check(context.Background())
		if b.Alive() != (code == 200) {
			t.Fatalf("status %d yielded alive=%v", code, b.Alive())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("health worker did not stop")
	}
}

func TestHealthTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	b, _ := balancer.NewBackend(srv.URL)
	b.SetAlive(true)
	c := &Checker{Backends: []*balancer.Backend{b}, Client: &http.Client{Timeout: 20 * time.Millisecond}}
	c.Check(context.Background())
	if b.Alive() {
		t.Fatal("timed-out backend remained healthy")
	}
}
