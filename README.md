# Proxy-Switcher

Сервис управления прокси через тг-бота. Служба принимает входящие соединения SOCKS5 или MTProto и маршрутизирует трафик через лучший доступный upstream-прокси на основе проверок. 

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
```

## ⚙️ Конфигурация

### Хранение данных

Сервис использует **SQLite** для хранения конфигурации upstream'ов.
- **Seed данных**: Upstream'ы из конфига загружаются только если БД пустая

### YAML конфигурация

Пример в `configs/config.default.yaml`: