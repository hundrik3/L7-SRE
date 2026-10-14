# Эксплуатация

## Отказ и восстановление

```sh
docker compose up --build -d --wait
docker compose stop backend2
sleep 3
curl -s http://localhost:9090/metrics | grep '^backend_status'
curl http://localhost:8080/
docker compose start backend2
sleep 3
curl -s http://localhost:9090/metrics | grep '^backend_status'
```

Gauge остановленного peer станет 0; backend1/3 продолжат обслуживать запросы.
После healthy probe peer возвращается, gauge — 1. Реальный failed attempt тоже
немедленно выводит peer из ротации. `/fail` возвращает 503 на всех demo peers:
успешный failover на этом endpoint ожидать нельзя.

## Rate limiting

```sh
RATE_LIMIT_RPS=1 RATE_LIMIT_BURST=2 \
  docker compose up -d --force-recreate lb
for i in 1 2 3 4; do curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8080/; done
curl -i http://localhost:8080/
curl -s http://localhost:9090/metrics | grep rate_limit_dropped_total
```

Два быстрых запроса проходят, следующие получают 429 и Retry-After. После refill
запрос снова разрешается. NAT-клиенты делят bucket. За ingress обязательно
настройте trusted CIDR, иначе bucket будет общим для IP ingress.

## Admin plane

| Endpoint | Поведение |
| --- | --- |
| `GET /healthz` | Liveness процесса, 200 даже без healthy upstream |
| `GET /readyz` | 200 при хотя бы одном healthy peer, иначе 503 |
| `GET /metrics` | Prometheus exposition |

Встроенной аутентификации нет. Compose публикует порты только на loopback хоста;
в deployment используйте network policy/security group. Независимая liveness
предотвращает бессмысленные restart loops при upstream outage.

## PromQL

RPS:

```promql
sum(rate(http_requests_total[5m]))
```

5xx ratio (при нулевом трафике отношение не имеет смысла):

```promql
sum(rate(http_requests_total{status_code=~"5.."}[5m]))
/ sum(rate(http_requests_total[5m]))
```

p95; для p50/p99 замените quantile на 0.5/0.99:

```promql
histogram_quantile(0.95,
  sum by (le) (rate(http_request_duration_seconds_bucket[5m]))
)
```

```promql
backend_status == 0
sum(rate(rate_limit_dropped_total[5m]))
```

Histogram включает весь data-plane запрос, в том числе 429 и response body.
Path агрегирован как proxy; methods нормализованы. Backend URL берётся только
из config. Request counter считает клиентские requests, не upstream attempts.

## Shutdown

```sh
docker compose stop lb
docker compose logs --tail=20 lb
make build
python3 scripts/smoke.py bin/l7lb
```

Compose даёт 30s на SIGTERM; внутренний default drain — 25s. Оркестратор должен
оставлять запас сверх внутреннего deadline. Smoke проверяет активный response.

## Build за HTTPS proxy

Для корпоративного CA:

```sh
docker build --secret id=proxy_ca,src=/path/to/trusted-ca-bundle.pem -t l7-sre-lb .
```

TLS и Go checksums остаются включёнными. CA mount не попадает в runtime image.
Dockerfile использует BuildKit module/build cache. Если Go proxy redirect на
object storage заблокирован политикой, получите dependency из разрешённого
GitHub через `GOPROXY=direct go mod download github.com/klauspost/compress@v1.17.9`,
затем `go mod download` и `go mod verify`. Для Docker тот же verified download
cache можно предварительно загрузить в BuildKit cache `l7-modules`; либо
разрешить object-storage destination через настройки сети. Отключать verification
или переносить Git credentials в image не нужно.
