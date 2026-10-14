# Производительность и проверка

```sh
go test -race -count=1 ./...
go vet ./...
go test ./internal/balancer ./internal/limiter \
  -run '^$' -bench . -benchmem -cpu 1,4 -count 5
go test ./internal/limiter -run '^$' -bench . \
  -mutexprofile mutex.prof -cpuprofile cpu.prof
go tool pprof cpu.prof
```

Тесты проверяют concurrent reservations, burst/refill, независимость IP, cleanup,
capacity, trust boundary XFF, health recovery, upstream timeout, retry policy,
client cancellation, lifetime response body и Prometheus exposition.
Smoke проверяет реальный binary, два listener и SIGTERM с активным запросом.

## Измеренный baseline

14 октября 2026, Linux amd64, Go 1.25.1, AMD EPYC 9V74.
Один прогон, `-benchtime=200ms`, `-cpu 1,4`. CPU=4 — четыре Go execution threads.

| Benchmark | CPU=1 ns/op | CPU=4 ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Round Robin, 3 peers | 46.44 | 89.41 | 24 | 1 |
| Least Connections, 3 peers | 75.48 | 107.9 | 24 | 1 |
| Round Robin, 32 peers | 46.23 | 84.99 | 24 | 1 |
| Least Connections, 32 peers | 278.5 | 386.8 | 24 | 1 |
| Round Robin, 128 peers | 47.86 | 83.88 | 24 | 1 |
| Least Connections, 128 peers | 1172 | 1497 | 24 | 1 |
| Token Bucket, shared IP | 90.32 | 116.1 | 0 | 0 |
| Token Bucket, multiple IPs | 78.78 | 111.2 | 0 | 0 |

Microbenchmarks не измеряют HTTP throughput или SLO. Multiple IPs раздаёт worker
ключ из пула до 1000 IP; реальное число ключей зависит от parallelism. Очень
высокий rate измеряет limiter hot path; cleanup и churn не входят в результаты.
Создание первого bucket амортизировано. Общий mutex заметен при конкуренции;
Least Connections растёт вместе с N. Lease выделяет 24 байта на attempt.

## HTTP load test

Если установлен wrk:

```sh
RATE_LIMIT_RPS=1000000 RATE_LIMIT_BURST=1000000 \
  docker compose up --build -d --wait
wrk -t4 -c128 -d30s --latency http://localhost:8080/
```

Фиксируйте hardware, Go version, strategy, число peers, response size,
concurrency, CPU/memory limits, error rate и limiter settings. Локальный Nginx
с коротким ответом не моделирует бизнес-нагрузку. Для сравнения стратегий нужен
сценарий с неоднородной latency peers.

HTTP load test с wrk здесь не заявляется выполненным. Подтверждены tests,
microbenchmarks и functional smoke; sustained-load нужно измерить отдельно.
Оптимизации после profiling: sharding limiter, incremental cleanup, global
in-flight limit, latency-aware balancing. Каждая требует измерений до/после.
