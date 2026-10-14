// Package health periodically probes all backends without overlapping rounds.
package health

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/hundrik3/L7-SRE/internal/balancer"
)

type Checker struct {
	Backends []*balancer.Backend
	Client   *http.Client
	Interval time.Duration
	OnStatus func(string, bool)
}

// Check runs one concurrent round and waits for every probe to finish.
func (c *Checker) Check(ctx context.Context) {
	var wg sync.WaitGroup
	for _, b := range c.Backends {
		wg.Add(1)
		go func(b *balancer.Backend) {
			defer wg.Done()
			u := b.URL()
			label := u.String()
			u.Path, u.RawPath = "/health", ""
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
			alive := false
			if err == nil {
				res, err := c.Client.Do(req)
				if err == nil {
					alive = res.StatusCode >= 200 && res.StatusCode < 300
					_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
					_ = res.Body.Close()
				}
			}
			if ctx.Err() != nil {
				return
			}
			b.SetAlive(alive)
			if c.OnStatus != nil {
				c.OnStatus(label, alive)
			}
		}(b)
	}
	wg.Wait()
}

// Run follows an initial synchronous Check performed before server startup.
func (c *Checker) Run(ctx context.Context) {
	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.Check(ctx)
		}
	}
}
