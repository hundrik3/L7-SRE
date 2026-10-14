//go:build !windows

package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestServerProcess runs the actual application in an isolated child process.
// Reusing the test executable also instruments the server in race-enabled runs.
func TestServerProcess(t *testing.T) {
	if os.Getenv("L7_SMOKE_HELPER") != "1" {
		return
	}
	main()
	os.Exit(0)
}

type smokeBackend struct {
	server *httptest.Server
	status atomic.Int64
}

func newSmokeBackends(t *testing.T, started chan struct{}, release <-chan struct{}) []*smokeBackend {
	t.Helper()
	backends := make([]*smokeBackend, 3)
	var once sync.Once
	for i := range backends {
		backend := &smokeBackend{}
		backend.status.Store(http.StatusOK)
		backend.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/slow" {
				once.Do(func() { close(started) })
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			w.WriteHeader(int(backend.status.Load()))
			_, _ = fmt.Fprintf(w, "backend%d\n", i+1)
		}))
		t.Cleanup(backend.server.Close)
		backends[i] = backend
	}
	return backends
}

type smokeProcess struct {
	cmd   *exec.Cmd
	done  chan struct{}
	err   error
	data  string
	admin string
}

func startSmokeProcess(t *testing.T, backends []*smokeBackend, overrides map[string]string) *smokeProcess {
	t.Helper()
	// Reserve both addresses at once so they cannot be the same ephemeral port.
	data, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	admin, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	urls := make([]string, len(backends))
	for i, b := range backends {
		urls[i] = b.server.URL
	}
	settings := map[string]string{
		"L7_SMOKE_HELPER": "1", "LISTEN_ADDR": data.Addr().String(), "ADMIN_ADDR": admin.Addr().String(),
		"BACKENDS": strings.Join(urls, ","), "BALANCER_STRATEGY": "round_robin", "MAX_ATTEMPTS": "3",
		"UPSTREAM_TIMEOUT": "5s", "DIAL_TIMEOUT": "1s", "HEALTH_INTERVAL": "50ms", "HEALTH_TIMEOUT": "200ms",
		"READ_HEADER_TIMEOUT": "5s", "READ_TIMEOUT": "15s", "WRITE_TIMEOUT": "20s", "IDLE_TIMEOUT": "60s",
		"SHUTDOWN_TIMEOUT": "3s", "RATE_LIMIT_RPS": "100", "RATE_LIMIT_BURST": "200",
		"RATE_LIMIT_MAX_CLIENTS": "1000", "RATE_LIMIT_IDLE_TTL": "1m", "RATE_LIMIT_CLEANUP_INTERVAL": "100ms",
		"TRUSTED_PROXY_CIDRS": "", "NO_PROXY": "127.0.0.1,localhost", "no_proxy": "127.0.0.1,localhost",
	}
	for key, value := range overrides {
		settings[key] = value
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestServerProcess$")
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := settings[key]; !replaced {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	for key, value := range settings {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	logPath := filepath.Join(t.TempDir(), "server.log")
	logs, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logs.Close() })
	cmd.Stdout, cmd.Stderr = logs, logs
	p := &smokeProcess{cmd: cmd, done: make(chan struct{}), data: "http://" + data.Addr().String(), admin: "http://" + admin.Addr().String()}
	_ = data.Close()
	_ = admin.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-p.done:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-p.done
			}
		}
		if t.Failed() {
			output, _ := os.ReadFile(logPath)
			t.Logf("server log:\n%s", output)
		}
	})
	return p
}

type smokeResponse struct {
	status int
	body   string
	header http.Header
	err    error
}

func smokeClient(t *testing.T) *http.Client {
	t.Helper()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// All test destinations are local; do not send them to an inherited proxy.
	transport.Proxy = nil
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}
}

func smokeGet(client *http.Client, url string, headers http.Header) smokeResponse {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return smokeResponse{err: err}
	}
	if headers != nil {
		req.Header = headers.Clone()
	}
	res, err := client.Do(req)
	if err != nil {
		return smokeResponse{err: err}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	return smokeResponse{status: res.StatusCode, body: string(body), header: res.Header, err: err}
}

func smokeRequire(t *testing.T, client *http.Client, url string, status int) smokeResponse {
	t.Helper()
	r := smokeGet(client, url, nil)
	if r.err != nil || r.status != status {
		t.Fatalf("GET %s: status=%d error=%v body=%s", url, r.status, r.err, r.body)
	}
	return r
}

func eventually(t *testing.T, description string, check func() bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if check() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", description)
		case <-ticker.C:
		}
	}
}

func TestSmokeFailoverAndShutdown(t *testing.T) {
	for _, strategy := range []string{"round_robin", "least_connections"} {
		t.Run(strategy, func(t *testing.T) {
			client := smokeClient(t)
			started, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			backends := newSmokeBackends(t, started, release)
			// Unblock a slow handler before httptest servers are closed on failure.
			t.Cleanup(unblock)
			p := startSmokeProcess(t, backends, map[string]string{"BALANCER_STRATEGY": strategy})
			eventually(t, "startup readiness", func() bool { r := smokeGet(client, p.admin+"/readyz", nil); return r.err == nil && r.status == 200 })
			for i := 0; i < 6; i++ {
				r := smokeRequire(t, client, p.data+"/", 200)
				if want := fmt.Sprintf("backend%d\n", i%3+1); r.body != want {
					t.Fatalf("got %q, want %q", r.body, want)
				}
			}
			metric := fmt.Sprintf("backend_status{backend_url=%q}", backends[0].server.URL)
			backends[0].status.Store(503)
			smokeRequire(t, client, p.data+"/", 200)
			eventually(t, "failed backend metric", func() bool {
				r := smokeGet(client, p.admin+"/metrics", nil)
				return r.err == nil && strings.Contains(r.body, metric+" 0")
			})
			for i := 0; i < 3; i++ {
				if smokeRequire(t, client, p.data+"/", 200).body == "backend1\n" {
					t.Fatal("failed backend remained in rotation")
				}
			}
			backends[0].status.Store(200)
			eventually(t, "backend recovery", func() bool {
				r := smokeGet(client, p.admin+"/metrics", nil)
				return r.err == nil && strings.Contains(r.body, metric+" 1")
			})
			found := false
			for i := 0; i < 3; i++ {
				found = smokeRequire(t, client, p.data+"/", 200).body == "backend1\n" || found
			}
			if !found {
				t.Fatal("recovered backend not selected")
			}
			for _, b := range backends {
				b.status.Store(503)
			}
			eventually(t, "unready state", func() bool { r := smokeGet(client, p.admin+"/readyz", nil); return r.err == nil && r.status == 503 })
			smokeRequire(t, client, p.data+"/", 503)
			smokeRequire(t, client, p.admin+"/healthz", 200)
			for _, b := range backends {
				b.status.Store(200)
			}
			eventually(t, "restored readiness", func() bool { r := smokeGet(client, p.admin+"/readyz", nil); return r.err == nil && r.status == 200 })
			result := make(chan smokeResponse, 1)
			go func() { result <- smokeGet(client, p.data+"/slow", nil) }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("slow request did not reach backend")
			}
			if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			eventually(t, "listener shutdown", func() bool {
				conn, err := net.DialTimeout("tcp", strings.TrimPrefix(p.data, "http://"), 50*time.Millisecond)
				if err != nil {
					return true
				}
				_ = conn.Close()
				return false
			})
			select {
			case <-p.done:
				t.Fatalf("server exited before active request completed: %v", p.err)
			default:
			}
			unblock()
			select {
			case r := <-result:
				if r.err != nil || r.status != 200 {
					t.Fatalf("active request interrupted: %+v", r)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("active request did not finish")
			}
			select {
			case <-p.done:
				if p.err != nil {
					t.Fatalf("shutdown: %v", p.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("server did not exit")
			}
		})
	}
}

func TestSmokeRateLimit(t *testing.T) {
	client := smokeClient(t)
	backends := newSmokeBackends(t, make(chan struct{}), nil)
	p := startSmokeProcess(t, backends, map[string]string{"RATE_LIMIT_RPS": "0.1", "RATE_LIMIT_BURST": "2"})
	eventually(t, "startup readiness", func() bool { r := smokeGet(client, p.admin+"/readyz", nil); return r.err == nil && r.status == 200 })
	for i := 0; i < 2; i++ {
		smokeRequire(t, client, p.data+"/", 200)
	}
	r := smokeGet(client, p.data+"/", http.Header{"X-Forwarded-For": {"203.0.113.99"}})
	if r.err != nil || r.status != 429 {
		t.Fatalf("spoofed XFF bypassed rate limit: %+v", r)
	}
	retry, err := strconv.Atoi(r.header.Get("Retry-After"))
	if err != nil || retry < 1 {
		t.Fatalf("invalid Retry-After: %s", r.header.Get("Retry-After"))
	}
	metrics := smokeRequire(t, client, p.admin+"/metrics", 200)
	if !strings.Contains(metrics.body, "rate_limit_dropped_total 1") {
		t.Fatal("missing drop metric")
	}
	if !strings.Contains(metrics.body, `http_requests_total{method="GET",path="proxy",status_code="429"} 1`) {
		t.Fatal("missing 429 request metric")
	}
	smokeRequire(t, client, p.admin+"/healthz", 200)
}
