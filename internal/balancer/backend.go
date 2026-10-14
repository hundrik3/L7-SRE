// Package balancer selects healthy backends and tracks in-flight requests.
package balancer

import (
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"
)

// Backend owns the state shared by request handlers and health checks.
// Create backends with NewBackend and do not copy them after first use.
type Backend struct {
	target url.URL
	mu     sync.RWMutex
	alive  bool
	active atomic.Int64
}

// NewBackend starts unhealthy until an initial health check succeeds.
func NewBackend(rawURL string) (*Backend, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse backend URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, fmt.Errorf("backend URL must be an absolute HTTP(S) URL without credentials, query or fragment")
	}
	return &Backend{target: *u}, nil
}

// URL returns a copy so callers cannot mutate the shared routing target.
func (b *Backend) URL() url.URL { return b.target }

func (b *Backend) Alive() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.alive
}

func (b *Backend) SetAlive(alive bool) {
	b.mu.Lock()
	b.alive = alive
	b.mu.Unlock()
}

// ActiveRequests counts HTTP requests, not TCP connections. This also works
// with HTTP/2 multiplexing and pooled upstream connections.
func (b *Backend) ActiveRequests() int64 { return b.active.Load() }
