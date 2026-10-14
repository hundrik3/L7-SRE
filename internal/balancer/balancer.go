package balancer

import (
	"errors"
	"fmt"
	"sync"
)

var ErrNoHealthyBackend = errors.New("no healthy backend available")

// Balancer reserves a backend for one upstream attempt. All requests sharing
// a pool must use the same balancer instance for consistent load accounting.
type Balancer interface {
	// Acquire skips unhealthy and excluded backends. Excluded is owned by the
	// caller and must not be mutated concurrently with this call.
	Acquire(excluded map[*Backend]struct{}) (*Lease, error)
}

// Lease holds a reservation until the upstream response body has been closed.
// Do not copy a Lease. Release is safe to call more than once.
type Lease struct {
	backend *Backend
	once    sync.Once
}

func (l *Lease) Backend() *Backend { return l.backend }

func (l *Lease) Release() {
	l.once.Do(func() { l.backend.active.Add(-1) })
}

type pool struct {
	mu       sync.Mutex
	backends []*Backend
	next     int
}

func newPool(backends []*Backend) (*pool, error) {
	if len(backends) == 0 {
		return nil, errors.New("backend pool must not be empty")
	}
	seen := make(map[string]struct{}, len(backends))
	for i, b := range backends {
		if b == nil {
			return nil, fmt.Errorf("backend %d is nil", i)
		}
		u := b.URL()
		key := u.String()
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate backend URL: %s", key)
		}
		seen[key] = struct{}{}
	}
	return &pool{backends: append([]*Backend(nil), backends...)}, nil
}

// acquire serializes selection and reservation so simultaneous requests do
// not all select the same least-loaded backend. Releases remain lock-free.
func (p *pool) acquire(excluded map[*Backend]struct{}, least bool) (*Lease, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	selected := -1
	var min int64
	for offset := 0; offset < len(p.backends); offset++ {
		i := (p.next + offset) % len(p.backends)
		b := p.backends[i]
		if _, skip := excluded[b]; skip || !b.Alive() {
			continue
		}
		load := b.ActiveRequests()
		if selected == -1 || load < min {
			selected, min = i, load
		}
		if !least {
			break
		}
	}
	if selected == -1 {
		return nil, ErrNoHealthyBackend
	}
	b := p.backends[selected]
	b.active.Add(1)
	p.next = (selected + 1) % len(p.backends)
	return &Lease{backend: b}, nil
}
