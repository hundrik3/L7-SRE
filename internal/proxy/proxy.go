// Package proxy provides bounded, replay-safe upstream failover.
package proxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/hundrik3/L7-SRE/internal/balancer"
	"github.com/hundrik3/L7-SRE/internal/limiter"
)

type Config struct {
	Balancer    balancer.Balancer
	Transport   http.RoundTripper
	MaxAttempts int
	Timeout     time.Duration
	Resolver    *limiter.IPResolver
	OnStatus    func(string, bool)
	Logger      *slog.Logger
}

func New(cfg Config) (http.Handler, error) {
	if cfg.Balancer == nil || cfg.Transport == nil || cfg.Resolver == nil || cfg.Logger == nil || cfg.MaxAttempts < 1 || cfg.Timeout <= 0 {
		return nil, errors.New("invalid proxy configuration")
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = "http", "balancer.invalid"
			pr.SetXForwarded()
			pr.Out.Header.Set("X-Forwarded-For", cfg.Resolver.ClientIP(pr.In))
		},
		Transport:  &retryTransport{cfg: cfg},
		BufferPool: &bufferPool{},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			status := http.StatusBadGateway
			var networkError net.Error
			if errors.Is(err, balancer.ErrNoHealthyBackend) {
				status = http.StatusServiceUnavailable
			} else if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout()) {
				status = http.StatusGatewayTimeout
			}
			if r.Context().Err() == nil {
				cfg.Logger.Warn("upstream request failed", "error", err, "status", status)
			}
			http.Error(w, http.StatusText(status), status)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hijacked connections are outside http.Server.Shutdown's ownership.
		if r.Header.Get("Upgrade") != "" || r.Method == http.MethodConnect {
			http.Error(w, "connection upgrades are not supported", http.StatusBadRequest)
			return
		}
		parent := context.WithValue(r.Context(), parentContextKey{}, r.Context())
		ctx, cancel := context.WithTimeout(parent, cfg.Timeout)
		defer cancel()
		rp.ServeHTTP(w, r.WithContext(ctx))
	}), nil
}

type retryTransport struct{ cfg Config }

type parentContextKey struct{}

func callerCanceled(ctx context.Context) bool {
	parent, ok := ctx.Value(parentContextKey{}).(context.Context)
	if ok {
		return parent.Err() != nil
	}
	return ctx.Err() != nil
}

func replayable(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return r.Body == nil || r.Body == http.NoBody || r.GetBody != nil
	default:
		return false
	}
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		defer req.Body.Close()
	}
	attempts := 1
	if replayable(req) {
		attempts = t.cfg.MaxAttempts
	}
	excluded := make(map[*balancer.Backend]struct{}, attempts)
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		lease, err := t.cfg.Balancer.Acquire(excluded)
		if err != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		backend := lease.Backend()
		excluded[backend] = struct{}{}
		out := req.Clone(req.Context())
		if attempt > 0 && req.Body != nil && req.Body != http.NoBody {
			out.Body, err = req.GetBody()
			if err != nil {
				lease.Release()
				return nil, err
			}
		}
		target := backend.URL()
		pr := httputil.ProxyRequest{Out: out}
		pr.SetURL(&target)
		res, err := t.cfg.Transport.RoundTrip(out)
		if err != nil {
			lease.Release()
			if !callerCanceled(req.Context()) {
				t.markDead(backend, target)
			}
			if req.Context().Err() != nil {
				return nil, req.Context().Err()
			}
			lastErr = err
			continue
		}
		if res.StatusCode >= 500 {
			t.markDead(backend, target)
			if attempt+1 < attempts {
				// Avoid draining an unbounded failure body before trying another peer.
				_ = res.Body.Close()
				lease.Release()
				lastErr = errors.New("upstream backends returned server errors")
				continue
			}
		}
		res.Body = &leasedBody{ReadCloser: res.Body, release: lease.Release, onError: func() {
			if !callerCanceled(req.Context()) {
				t.markDead(backend, target)
			}
		}}
		return res, nil
	}
	return nil, lastErr
}

func (t *retryTransport) markDead(b *balancer.Backend, target url.URL) {
	b.SetAlive(false)
	if t.cfg.OnStatus != nil {
		t.cfg.OnStatus(target.String(), false)
	}
}

type leasedBody struct {
	io.ReadCloser
	release func()
	once    sync.Once
	onError func()
}

func (b *leasedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF {
		b.onError()
	}
	return n, err
}

func (b *leasedBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}

type bufferPool struct{ pool sync.Pool }

func (p *bufferPool) Get() []byte {
	if b := p.pool.Get(); b != nil {
		return b.([]byte)
	}
	return make([]byte, 32*1024)
}
func (p *bufferPool) Put(b []byte) { p.pool.Put(b) }
