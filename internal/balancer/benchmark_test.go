package balancer

import (
	"fmt"
	"testing"
)

func BenchmarkBalancer(b *testing.B) {
	for _, size := range []int{3, 32, 128} {
		backends := make([]*Backend, size)
		for i := range backends {
			backend, err := NewBackend(fmt.Sprintf("http://backend%d:8080", i))
			if err != nil {
				b.Fatal(err)
			}
			backend.SetAlive(true)
			backends[i] = backend
		}
		for _, strategy := range []string{"round_robin", "least_connections"} {
			b.Run(fmt.Sprintf("%s/%d", strategy, size), func(b *testing.B) {
				var lb Balancer
				var err error
				if strategy == "round_robin" {
					lb, err = NewRoundRobin(backends)
				} else {
					lb, err = NewLeastConnections(backends)
				}
				if err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						lease, err := lb.Acquire(nil)
						if err != nil {
							b.Error(err)
							return
						}
						lease.Release()
					}
				})
			})
		}
	}
}
