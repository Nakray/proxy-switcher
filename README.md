# Proxy-Switcher

Сервис управления прокси. Служба принимает входящие соединения SOCKS5 или MTProto и маршрутизирует трафик через лучший доступный upstream-прокси на основе проверок.

## 🚀 Возможности

- **Два протокола**: Поддержка входящих соединений SOCKS5 и MTProto
- **Health Check**: Автоматический мониторинг здоровья upstream-прокси
- **Умная маршрутизация**: Выбор лучшего прокси по задержке (latency)
- **Автоматический failover**: Переподключение при смене upstream
- **Prometheus метрики**: 13 метрик для мониторинга
- **Telegram бот**: Управление прокси и алерты

## ⚡ Быстрый старт

### Через Docker Compose

1. Клонировать репозиторий:
```bash
git clone https://github.com/Nakray/proxy-switcher.git
cd proxy-switcher
```

2. Скопировать и настроить конфигурацию:
```bash
cp configs/config.example.yaml configs/config.yaml
# Отредактируйте configs/config.yaml
```

3. Запустить сервис:
```bash
docker-compose up -d
```

4. С мониторингом (Prometheus + Grafana):
```bash
docker-compose --profile monitoring up -d
```

### Ручная сборка

```bash
# Сборка
go build -o proxy-switcher ./cmd/

# Запуск с конфигом
./proxy-switcher -config configs/config.yaml

# Или через переменные окружения
export PROXY_SOCKS5_PORT=1080
export BOT_TOKEN="ваш-токен"
./proxy-switcher
```

## ⚙️ Конфигурация

### Хранение данных

Сервис использует **SQLite** для хранения конфигурации upstream'ов.
- **Seed данных**: Upstream'ы из конфига загружаются только если БД пустая

### YAML конфигурация

Пример в `configs/config.default.yaml`:

## 🤖 Telegram бот

### Команды статуса

| Команда | Описание |
|---------|----------|
| `/start` или `/help` | Показать справку |
| `/status` | Текущий статус прокси |
| `/upstreams` | Список upstream'ов со статусом |
| `/metrics` | Сводка метрик |

### Команды управления

| Команда | Описание |
|---------|----------|
| `/manage` | Интерактивное меню |
| `/add <name> <type> <host> <port> [user] [pass]` | Добавить upstream |
| `/remove <name>` | Удалить upstream |
| `/enable <name>` | Включить upstream |
| `/disable <name>` | Отключить upstream |

**Примеры:**
```
/add myproxy socks5 proxy.example.com 1080 user pass
/add mtproxy mtproto mt.example.com 443
/enable myproxy
/disable myproxy
/remove myproxy
```

### Интерактивное меню

Команда `/manage` открывает меню с кнопками:
- ⏸️/▶️ — Отключить/Включить прокси
- 🗑️ — Удалить (с подтверждением)
- 🔄 — Обновить статус

### Статусы upstream'ов

- 🟢 — Здоров и включён
- 🔴 — Нездоров (failed health check)
- ⚪ — Отключён вручную

## 📊 Метрики

| Метрика | Описание |
|---------|----------|
| `proxy_active_connections` | Активные соединения |
| `proxy_total_connections` | Всего соединений |
| `proxy_connection_duration_seconds` | Длительность соединений |
| `proxy_bytes_transferred_total` | Переданные байты |
| `upstream_latency_milliseconds` | Задержка upstream'ов |
| `upstream_health_status` | Статус (1=здоров, 0=нет) |
| `upstream_requests_total` | Запросы к upstream |
| `upstream_failures_total` | Ошибки upstream |
| `upstream_reconnects_total` | Переподключения |
| `health_check_duration_seconds` | Длительность проверок |
| `bot_messages_sent_total` | Сообщений бота |
| `bot_commands_total` | Команд бота |

### Grafana дашборд

Включает:
- Активные/всего соединений
- Переданные байты
- Статус upstream'ов
- Графики задержек
- Частота запросов
- Частота ошибок


## 🌐 API эндпоинты

| Эндпоинт | Описание |
|----------|----------|
| `GET /metrics` | Prometheus метрики |
| `GET /health` | Проверка здоровья |

## 🛠️ Разработка

### Запуск тестов

```bash
go test ./...
```

### Сборка Docker образа

```bash
docker build -t proxy-switcher .
```

### Тестовый скрипт

```bash
./scripts/test.sh
```

### Мониторинг

1. Настройте Prometheus на сбор метрик
2. Импортируйте дашборд Grafana из `deploy/grafana/`
3. Настройте алерты:
   - Все upstream'ы недоступны
   - Высокий процент ошибок
   - Высокая задержка

## 📝 Логи

Уровни логирования: `debug`, `info`, `warn`, `error`

Пример просмотра логов:
```bash
docker-compose logs -f proxy-switcher
```
