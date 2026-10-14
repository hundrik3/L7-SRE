package balancer

// LeastConnections selects the backend with the fewest in-flight requests.
// Ties rotate to avoid favoring the first backend in the pool.
type LeastConnections struct{ pool *pool }

func NewLeastConnections(backends []*Backend) (*LeastConnections, error) {
	p, err := newPool(backends)
	if err != nil {
		return nil, err
	}
	return &LeastConnections{pool: p}, nil
}

func (l *LeastConnections) Acquire(excluded map[*Backend]struct{}) (*Lease, error) {
	return l.pool.acquire(excluded, true)
}

var _ Balancer = (*LeastConnections)(nil)
