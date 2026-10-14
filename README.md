# High-Performance L7 Load Balancer & Rate Limiter

HTTP reverse proxy на Go 1.22+: Round Robin, Least Connections, active health
checking, безопасный failover, Token Bucket по IP и Prometheus-метрики.
Сетевой стек и синхронизация построены на стандартной библиотеке. Единственная
прямая внешняя зависимость — официальный `prometheus/client_golang`.

## Быстрый запуск

Нужны Docker Engine и Docker Compose v2:

```sh
docker compose up --build -d --wait
curl http://localhost:8080/
curl http://localhost:9090/readyz
curl http://localhost:9090/metrics
```

Стенд содержит балансировщик и три Nginx-бэкенда. Последовательные ответы
`backend1`, `backend2`, `backend3` показывают Round Robin.

```sh
for i in 1 2 3 4 5 6; do curl -s http://localhost:8080/; sleep 0.1; done

# Select Least Connections.
BALANCER_STRATEGY=least_connections docker compose up -d --force-recreate lb

# Optional Prometheus UI on port 9091.
docker compose --profile observability up -d --wait

docker compose --profile observability down
```

Dockerfile собирает статический бинарник отдельным Go-этапом. Runtime — Alpine
с непривилегированным пользователем и корневыми CA для HTTPS-бэкендов. Контейнер
балансировщика работает с файловой системой только для чтения.

## Структура

```text
cmd/l7lb/main.go          # Configuration, HTTP servers, workers, shutdown
cmd/l7lb/smoke_test.go    # Process-level integration and SIGTERM tests
internal/balancer/       # Backend state, reservations, two strategies
internal/limiter/        # Token buckets, cleanup, trusted client IP resolution
internal/health/         # Concurrent active probes
internal/proxy/          # ReverseProxy, transport, retry policy, buffer pool
internal/metrics/        # Prometheus registry and HTTP instrumentation
internal/config/         # Environment parsing and validation
config/example.env       # Complete local configuration example
deploy/nginx/            # Demo backend configuration
deploy/prometheus/       # Optional scrape configuration
docs/                    # Design, configuration, operations, performance
.github/workflows/ci.yml  # Go 1.22, race tests, vet, build
Dockerfile
docker-compose.yml
Makefile
```

## Локальная разработка

```sh
go mod download
make build
make test
make vet
make bench
make smoke
```

Smoke-тест самостоятельно запускает три HTTP-бэкенда, проверяет failover,
восстановление, rate limiting, Prometheus и SIGTERM с активным запросом.
Он написан на Go и входит в `go test -race ./...`; дополнительный runtime не нужен.
Тесты запускают приложение отдельным процессом из test executable, включая
race instrumentation. На Windows signal-based smoke-тесты пропускаются.

Для собственного окружения запустите бэкенды и загрузите настройки:

```sh
set -a
. config/example.env
set +a
./bin/l7lb
```

При нестандартной установке Go: `make GO=/path/to/go test`.

## Возможности

| Компонент | Поведение |
| --- | --- |
| Round Robin | Циклический выбор доступных бэкендов |
| Least Connections | Минимум активных HTTP-запросов, циклическое разрешение равенства |
| Failover | Transport error/5xx выводят peer из ротации; ограниченные ретраи GET/HEAD/OPTIONS |
| Health checks | `/health`, начальная проверка до приёма трафика, конкурентные проверки |
| Token Bucket | Rate и burst на IP, idle eviction, верхняя граница количества клиентов |
| Client IP | `RemoteAddr`, доверенные CIDR и разбор XFF справа налево |
| Метрики | Запросы, длительность, статус peer, отказы limiter, Go/process collectors |
| Shutdown | SIGINT/SIGTERM, drain двух серверов, остановка workers |

## Документация

- [Архитектура и конкурентность](docs/architecture.md): владение состоянием, reservation, HTTP/2, ретраи.
- [Настройки](docs/configuration.md): все переменные, defaults, доверенные прокси.
- [Эксплуатация](docs/operations.md): отказ, восстановление, 429, PromQL, shutdown.
- [Производительность](docs/performance.md): команды, измерения, ограничения benchmarks.

## Границы проекта

Это портфолио-проект с работающим HTTP-сервером, а не замена Envoy или HAProxy.
Нет распределённого rate limiting, TLS termination, service discovery, weighted
balancing и WebSocket/CONNECT. HTTPS к upstream поддерживается; входящий TLS
предполагается завершать на ingress. Admin-порт нужно ограничить сетевыми
правилами: встроенной аутентификации `/metrics` нет.

Произвольные URL агрегируются в `path="proxy"`, чтобы не создавать отдельный
временной ряд для каждого ID или атакующего URL. POST и другие операции записи
не ретраятся. Общий upstream timeout включает все attempts и response body;
неограниченные потоковые ответы не поддерживаются. Подробнее — в документации.
