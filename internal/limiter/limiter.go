// Package limiter implements per-client token buckets with bounded storage.
package limiter

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"
)

type Config struct {
	Rate            float64
	Burst           int
	IdleTTL         time.Duration
	CleanupInterval time.Duration
	MaxClients      int
}

type bucket struct {
	tokens  float64
	updated time.Time
	seen    time.Time
}

type Limiter struct {
	mu      sync.Mutex
	clients map[string]*bucket
	cfg     Config
}

func New(cfg Config) (*Limiter, error) {
	if cfg.Rate <= 0 || math.IsNaN(cfg.Rate) || math.IsInf(cfg.Rate, 0) || cfg.Burst < 1 || cfg.MaxClients < 1 || cfg.IdleTTL <= 0 || cfg.CleanupInterval <= 0 {
		return nil, fmt.Errorf("invalid token bucket configuration")
	}
	// Eviction must not refill a bucket faster than its normal refill rate.
	if cfg.IdleTTL.Seconds() < float64(cfg.Burst)/cfg.Rate {
		return nil, fmt.Errorf("idle TTL must cover a full bucket refill")
	}
	return &Limiter{clients: make(map[string]*bucket), cfg: cfg}, nil
}

// Allow consumes one token and returns a conservative retry delay on rejection.
func (l *Limiter) Allow(client string) (bool, time.Duration) {
	return l.allowAt(client, time.Now())
}

func (l *Limiter) allowAt(client string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.clients[client]
	if !ok {
		if len(l.clients) >= l.cfg.MaxClients {
			return false, l.cfg.CleanupInterval
		}
		b = &bucket{tokens: float64(l.cfg.Burst), updated: now, seen: now}
		l.clients[client] = b
	}
	if now.After(b.updated) {
		b.tokens = math.Min(float64(l.cfg.Burst), b.tokens+now.Sub(b.updated).Seconds()*l.cfg.Rate)
		b.updated = now
	}
	if now.After(b.seen) {
		b.seen = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	seconds := (1 - b.tokens) / l.cfg.Rate
	if seconds >= float64(math.MaxInt64)/float64(time.Second) {
		return false, time.Duration(math.MaxInt64)
	}
	return false, time.Duration(math.Ceil(seconds * float64(time.Second)))
}

// Run owns the cleanup ticker and exits when the application context ends.
func (l *Limiter) Run(ctx context.Context) {
	ticker := time.NewTicker(l.cfg.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			l.cleanup(now)
		}
	}
}

func (l *Limiter) cleanup(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, b := range l.clients {
		if now.Sub(b.seen) >= l.cfg.IdleTTL {
			delete(l.clients, key)
		}
	}
}
