# L7 Load Balancer

A Go 1.22+ HTTP load balancer with explicit upstream request accounting.

## Project layout

```text
cmd/l7lb/main.go                  # Composition root (planned)
internal/balancer/               # Implemented: backend state and selection
internal/limiter/                # Token bucket and idle client cleanup (planned)
internal/health/                 # Active health checks (planned)
internal/proxy/                  # Reverse proxy and bounded retries (planned)
internal/metrics/                # Prometheus collectors and middleware (planned)
internal/config/                 # Environment configuration and validation (planned)
config/                         # Example configuration (planned)
deploy/nginx/                    # Demo backend configuration (planned)
Dockerfile                      # Multi-stage build (planned)
docker-compose.yml              # Load balancer and three demo backends (planned)
go.mod
```

## Backend selection

Construct backend instances once and share one balancer instance between all
request handlers. Backends start unhealthy; the initial health check must mark
them healthy before accepting traffic. Round robin cycles through eligible
backends. Least connections measures in-flight HTTP requests, including streamed
responses, rather than TCP sockets. Ties rotate between backends.

`Acquire` selects and reserves a backend under the pool mutex. The reservation
uses an atomic request counter. Call `Lease.Release` after an upstream attempt
has finished and its response body has been closed, including failed attempts.
Release is idempotent. A request-local exclusion set prevents retries from
revisiting the same backend. Health changes affect subsequent selections;
already reserved attempts are allowed to finish.

The proxy layer will own retry policy. Retry only requests whose method and body
can safely be replayed, and only before downstream response headers are sent.
Do not blindly replay writes after a timeout or a server error: the upstream may
already have committed them.

## Validation

```sh
go test -race ./...
go vet ./...
```

The HTTP server, limiter, health worker, metrics and demo containers will be
implemented in the following steps. There is no runnable server yet.
