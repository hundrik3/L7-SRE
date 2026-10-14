package limiter

import (
	"context"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testLimiter(t testing.TB, rate float64, burst int) *Limiter {
	t.Helper()
	l, err := New(Config{Rate: rate, Burst: burst, IdleTTL: time.Hour, CleanupInterval: time.Minute, MaxClients: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestRefillAndCleanup(t *testing.T) {
	l := testLimiter(t, 2, 2)
	now := time.Unix(1000, 0)
	for i := 0; i < 2; i++ {
		if ok, _ := l.allowAt("a", now); !ok {
			t.Fatal("initial burst rejected")
		}
	}
	if ok, retry := l.allowAt("a", now); ok || retry != 500*time.Millisecond {
		t.Fatalf("got %v, %v", ok, retry)
	}
	if ok, _ := l.allowAt("b", now); !ok {
		t.Fatal("clients share tokens")
	}
	if ok, _ := l.allowAt("a", now.Add(500*time.Millisecond)); !ok {
		t.Fatal("refill failed")
	}
	l.cleanup(now.Add(time.Hour))
	if len(l.clients) != 1 {
		t.Fatalf("got %d entries, want one recently used client", len(l.clients))
	}
	l.cleanup(now.Add(time.Hour + time.Second))
	if len(l.clients) != 0 {
		t.Fatal("idle entries retained")
	}
}

func TestConcurrentBurst(t *testing.T) {
	l := testLimiter(t, 1, 50)
	now := time.Unix(1000, 0)
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := l.allowAt("a", now); ok {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 50 {
		t.Fatalf("accepted %d, want 50", accepted.Load())
	}
}

func TestStorageBoundAndCancellation(t *testing.T) {
	l := testLimiter(t, 1, 1)
	l.cfg.MaxClients = 1
	now := time.Unix(1000, 0)
	l.allowAt("a", now)
	if ok, _ := l.allowAt("b", now); ok {
		t.Fatal("storage cap ignored")
	}
	l.cleanup(now.Add(time.Hour))
	if ok, _ := l.allowAt("b", now.Add(time.Hour)); !ok {
		t.Fatal("eviction did not free capacity")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup worker did not stop")
	}
}

func TestClientIPTrust(t *testing.T) {
	r, err := NewIPResolver([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ peer, forwarded, want string }{
		{"192.0.2.1:1234", "203.0.113.9", "192.0.2.1"},
		{"10.0.0.1:1234", "198.51.100.1, 10.0.0.2", "198.51.100.1"},
		{"10.0.0.1:1234", "203.0.113.9, 198.51.100.1", "198.51.100.1"},
		{"10.0.0.1:1234", "invalid, 10.0.0.2", "10.0.0.1"},
		{"[::ffff:192.0.2.1]:1234", "", "192.0.2.1"},
	} {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = tc.peer
		req.Header.Set("X-Forwarded-For", tc.forwarded)
		if got := r.ClientIP(req); got != tc.want {
			t.Errorf("got %s, want %s", got, tc.want)
		}
	}
}

func TestInvalidConfig(t *testing.T) {
	if _, err := New(Config{Rate: 1, Burst: 10, IdleTTL: time.Second, CleanupInterval: time.Second, MaxClients: 1}); err == nil {
		t.Fatal("unsafe idle TTL accepted")
	}
}

func BenchmarkLimiter(b *testing.B) {
	for _, clients := range []int{1, 1000} {
		name := "shared_ip"
		if clients > 1 {
			name = "many_ips"
		}
		b.Run(name, func(b *testing.B) {
			l := testLimiter(b, 1e9, 1000)
			keys := make([]string, clients)
			for i := range keys {
				keys[i] = time.Unix(int64(i), 0).String()
			}
			var worker atomic.Uint64
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				key := keys[int(worker.Add(1)-1)%len(keys)]
				for pb.Next() {
					l.Allow(key)
				}
			})
		})
	}
}
