package balancer

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func testBackends(t *testing.T) []*Backend {
	t.Helper()
	backends := make([]*Backend, 3)
	for i := range backends {
		b, err := NewBackend(fmt.Sprintf("http://backend%d:8080", i))
		if err != nil {
			t.Fatal(err)
		}
		b.SetAlive(true)
		backends[i] = b
	}
	return backends
}

func TestRoundRobinEligibility(t *testing.T) {
	backends := testBackends(t)
	r, err := NewRoundRobin(backends)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 9; i++ {
		lease, err := r.Acquire(nil)
		if err != nil {
			t.Fatal(err)
		}
		if lease.Backend() != backends[i%3] {
			t.Fatal("unexpected round robin order")
		}
		lease.Release()
		lease.Release()
	}
	backends[0].SetAlive(false)
	excluded := map[*Backend]struct{}{backends[1]: {}}
	lease, err := r.Acquire(excluded)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Backend() != backends[2] {
		t.Fatal("selected an ineligible backend")
	}
	lease.Release()
	backends[2].SetAlive(false)
	if _, err := r.Acquire(excluded); !errors.Is(err, ErrNoHealthyBackend) {
		t.Fatalf("got %v, want ErrNoHealthyBackend", err)
	}
	backends[0].SetAlive(true)
	lease, err = r.Acquire(excluded)
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	for _, b := range backends {
		if b.ActiveRequests() != 0 {
			t.Fatal("leaked reservation")
		}
	}
}

func TestLeastConnectionsConcurrentReservations(t *testing.T) {
	backends := testBackends(t)
	l, err := NewLeastConnections(backends)
	if err != nil {
		t.Fatal(err)
	}
	const requests = 300
	leases := make([]*Lease, requests)
	var wg sync.WaitGroup
	for i := range leases {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lease, err := l.Acquire(nil)
			if err != nil {
				t.Error(err)
				return
			}
			leases[i] = lease
		}(i)
	}
	wg.Wait()
	for _, b := range backends {
		if got := b.ActiveRequests(); got != requests/3 {
			t.Fatalf("got %d active requests, want %d", got, requests/3)
		}
	}
	for _, lease := range leases {
		if lease != nil {
			lease.Release()
		}
	}
	for _, b := range backends {
		if b.ActiveRequests() != 0 {
			t.Fatal("leaked reservation")
		}
	}
}
