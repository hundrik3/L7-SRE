package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/hundrik3/L7-SRE/internal/balancer"
	"github.com/hundrik3/L7-SRE/internal/config"
	"github.com/hundrik3/L7-SRE/internal/health"
	"github.com/hundrik3/L7-SRE/internal/limiter"
	"github.com/hundrik3/L7-SRE/internal/metrics"
	"github.com/hundrik3/L7-SRE/internal/proxy"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("load balancer stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	backends := make([]*balancer.Backend, 0, len(cfg.Backends))
	for _, raw := range cfg.Backends {
		b, err := balancer.NewBackend(raw)
		if err != nil {
			return err
		}
		backends = append(backends, b)
	}
	var lb balancer.Balancer
	if cfg.Strategy == "least_connections" {
		lb, err = balancer.NewLeastConnections(backends)
	} else {
		lb, err = balancer.NewRoundRobin(backends)
	}
	if err != nil {
		return err
	}
	rl, err := limiter.New(cfg.Limiter)
	if err != nil {
		return err
	}
	resolver, err := limiter.NewIPResolver(cfg.TrustedProxies)
	if err != nil {
		return err
	}
	m := metrics.New()
	for _, b := range backends {
		u := b.URL()
		m.SetBackend(u.String(), false)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: cfg.DialTimeout, KeepAlive: 30 * time.Second}).DialContext
	transport.ResponseHeaderTimeout = cfg.UpstreamTimeout / time.Duration(cfg.MaxAttempts)
	transport.MaxIdleConns, transport.MaxIdleConnsPerHost = 256, 64
	transport.MaxConnsPerHost = 256
	defer transport.CloseIdleConnections()
	healthTransport := transport.Clone()
	defer healthTransport.CloseIdleConnections()
	checker := &health.Checker{Backends: backends, Client: &http.Client{Transport: healthTransport, Timeout: cfg.HealthTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Interval: cfg.HealthInterval, OnStatus: m.SetBackend}
	handler, err := proxy.New(proxy.Config{Balancer: lb, Transport: transport, MaxAttempts: cfg.MaxAttempts, Timeout: cfg.UpstreamTimeout, Resolver: resolver, OnStatus: m.SetBackend, Logger: logger})
	if err != nil {
		return err
	}
	dataMux := http.NewServeMux()
	dataMux.Handle("/", m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if allowed, retry := rl.Allow(resolver.ClientIP(r)); !allowed {
			m.Dropped.Inc()
			w.Header().Set("Retry-After", strconv.FormatInt(int64(math.Max(1, math.Ceil(retry.Seconds()))), 10))
			http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
			return
		}
		handler.ServeHTTP(w, r)
	})))
	adminMux := http.NewServeMux()
	adminMux.Handle("GET /metrics", m.Handler())
	adminMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	adminMux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		for _, b := range backends {
			if b.Alive() {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("ready\n"))
				return
			}
		}
		http.Error(w, "no healthy backend", http.StatusServiceUnavailable)
	})
	server := &http.Server{Addr: cfg.ListenAddr, Handler: dataMux, ReadHeaderTimeout: cfg.ReadHeaderTimeout, ReadTimeout: cfg.ReadTimeout, WriteTimeout: cfg.WriteTimeout, IdleTimeout: cfg.IdleTimeout, MaxHeaderBytes: 1 << 20}
	admin := &http.Server{Addr: cfg.AdminAddr, Handler: adminMux, ReadHeaderTimeout: cfg.ReadHeaderTimeout, ReadTimeout: cfg.ReadTimeout, WriteTimeout: cfg.WriteTimeout, IdleTimeout: cfg.IdleTimeout}
	// Bind both listeners before starting workers to fail startup atomically.
	dataListener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return err
	}
	defer dataListener.Close()
	adminListener, err := net.Listen("tcp", cfg.AdminAddr)
	if err != nil {
		return err
	}
	defer adminListener.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	defer cancelWorkers()
	checker.Check(ctx)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() { defer workers.Done(); checker.Run(workerCtx) }()
	go func() { defer workers.Done(); rl.Run(workerCtx) }()
	serveErrors := make(chan error, 2)
	go func() { serveErrors <- server.Serve(dataListener) }()
	go func() { serveErrors <- admin.Serve(adminListener) }()
	logger.Info("load balancer started", "listen", cfg.ListenAddr, "admin", cfg.AdminAddr, "strategy", cfg.Strategy, "backends", len(backends))
	select {
	case <-ctx.Done():
	case err = <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	stop()
	logger.Info("draining active requests")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	var shutdown sync.WaitGroup
	shutdownErrors := make(chan error, 2)
	for _, s := range []*http.Server{server, admin} {
		shutdown.Add(1)
		go func(s *http.Server) {
			defer shutdown.Done()
			if e := s.Shutdown(shutdownCtx); e != nil {
				_ = s.Close()
				shutdownErrors <- e
			}
		}(s)
	}
	shutdown.Wait()
	cancelWorkers()
	workers.Wait()
	close(shutdownErrors)
	for e := range shutdownErrors {
		err = errors.Join(err, fmt.Errorf("shutdown: %w", e))
	}
	return err
}
