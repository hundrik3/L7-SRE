# syntax=docker/dockerfile:1
FROM golang:1.25.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
# Optional trust bundle for development environments with an HTTPS proxy.
RUN --mount=type=cache,id=l7-modules,target=/go/pkg/mod --mount=type=secret,id=proxy_ca \
    if [ -f /run/secrets/proxy_ca ]; then export SSL_CERT_FILE=/run/secrets/proxy_ca; fi; \
    go mod download && go mod verify
COPY . .
RUN --mount=type=cache,id=l7-modules,target=/go/pkg/mod \
    --mount=type=cache,id=l7-build,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/l7lb ./cmd/l7lb

FROM alpine:3.22
RUN addgroup -S app && adduser -S -G app app
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/l7lb /usr/local/bin/l7lb
USER app
EXPOSE 8080 9090
HEALTHCHECK --interval=5s --timeout=2s --start-period=5s --retries=3 \
    CMD wget -Y off -q -O /dev/null http://127.0.0.1:9090/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/l7lb"]
