GO ?= go

.PHONY: build run test smoke vet bench fmt demo demo-down

build:
	$(GO) build -trimpath -o bin/l7lb ./cmd/l7lb

run:
	$(GO) run ./cmd/l7lb

test:
	$(GO) test -race -count=1 ./...

smoke:
	$(GO) test -race -count=1 -v ./cmd/l7lb -run '^TestSmoke'

vet:
	$(GO) vet ./...

bench:
	$(GO) test ./internal/balancer ./internal/limiter -run '^$$' -bench . -benchmem -cpu 1,4

fmt:
	$(GO) fmt ./...

demo:
	docker compose up --build -d --wait

demo-down:
	docker compose down
