package balancer

// RoundRobin distributes attempts cyclically across eligible backends.
type RoundRobin struct{ pool *pool }

func NewRoundRobin(backends []*Backend) (*RoundRobin, error) {
	p, err := newPool(backends)
	if err != nil {
		return nil, err
	}
	return &RoundRobin{pool: p}, nil
}

func (r *RoundRobin) Acquire(excluded map[*Backend]struct{}) (*Lease, error) {
	return r.pool.acquire(excluded, false)
}

var _ Balancer = (*RoundRobin)(nil)
