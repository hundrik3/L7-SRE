# Конфигурация

Environment читается при startup. Обязательна `BACKENDS`. Явно пустое значение
не заменяется default. Некорректные значения останавливают startup с ошибкой.

| Variable | Default | Назначение |
| --- | --- | --- |
| `BACKENDS` | обязательно | HTTP(S) URL через запятую; base path допустим |
| `LISTEN_ADDR` | `:8080` | Data plane |
| `ADMIN_ADDR` | `:9090` | Admin plane |
| `BALANCER_STRATEGY` | `round_robin` | `round_robin` / `least_connections` |
| `MAX_ATTEMPTS` | `3` | Все attempts, от 1 до 10 |
| `UPSTREAM_TIMEOUT` | `5s` | Все upstream attempts и response body |
| `DIAL_TIMEOUT` | `1s` | Соединение |
| `HEALTH_INTERVAL` | `5s` | Интервал health rounds |
| `HEALTH_TIMEOUT` | `1s` | Максимальная длительность probe |
| `READ_HEADER_TIMEOUT` | `5s` | Чтение headers |
| `READ_TIMEOUT` | `15s` | Чтение headers и request body |
| `WRITE_TIMEOUT` | `20s` | Ответ; больше upstream timeout |
| `IDLE_TIMEOUT` | `60s` | Keep-alive idle |
| `SHUTDOWN_TIMEOUT` | `25s` | Drain до forced close |
| `RATE_LIMIT_RPS` | `100` | Tokens/s на IP, положительное finite число |
| `RATE_LIMIT_BURST` | `200` | Начальное и максимальное число tokens |
| `RATE_LIMIT_MAX_CLIENTS` | `100000` | Максимум записей IP |
| `RATE_LIMIT_IDLE_TTL` | `5m` | Idle eviction, не меньше burst/rate |
| `RATE_LIMIT_CLEANUP_INTERVAL` | `1m` | Cleanup period |
| `TRUSTED_PROXY_CIDRS` | пусто | Доверенные proxy networks через запятую |

Адреса серверов должны быть доступны для bind и не совпадать. Backend URL:
абсолютный HTTP(S), без credentials/query/fragment; дубликаты запрещены.
Transport использует системные корневые CA и стандартные proxy environment
variables. `NO_PROXY` должен исключать локальные upstream из внешнего proxy.
Compose задаёт его для backend1–3.

## Client IP

Без trusted CIDR используется только RemoteAddr: пользователь не может сменить
bucket через XFF. При доверенном direct peer XFF разбирается справа налево до
первого недоверенного IP. Некорректная или слишком длинная цепочка целиком
игнорируется. IPv4-mapped IPv6 нормализуется в IPv4.

```text
client 198.51.100.7 → ingress 10.0.0.2 → load balancer
TRUSTED_PROXY_CIDRS=10.0.0.0/24
X-Forwarded-For: 198.51.100.7
```

Доверяйте только сетям реальных ingress; ingress должен добавлять адрес клиента.
`0.0.0.0/0` позволяет подделать IP. На upstream передаётся нормализованный XFF.
Forwarded/X-Forwarded-Host/Proto очищаются и формируются заново. Внешняя TLS
topology автоматически не доверяется: схема определяется прямым соединением.

## Demo settings

Compose: rate 10/s, burst 20, health interval 2s. Override:

```sh
RATE_LIMIT_RPS=1000 RATE_LIMIT_BURST=2000 \
  docker compose up -d --force-recreate lb
```

`config/example.env` предназначен для native startup, Compose его сам не читает.
