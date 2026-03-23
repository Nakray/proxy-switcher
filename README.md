# Telegram Proxy Manager

[🇷🇺 Русская версия](README_ru.md)

High-performance Telegram Proxy Manager service written in Go. This service acts as a proxy switcher, accepting incoming connections from Telegram clients via SOCKS5 or MTProto and routing them through the best available upstream proxy based on health checks.

## Features

- **Dual Protocol Support**: Accepts incoming connections via SOCKS5 and MTProto
- **Upstream Health Checking**: Automatic health monitoring of upstream proxies with configurable intervals
- **Smart Routing**: Routes traffic through the healthiest upstream with lowest latency
- **Automatic Failover**: Reconnection logic with automatic upstream switching on failure
- **Prometheus Metrics**: Comprehensive metrics for monitoring (connections, latency, bytes transferred, upstream status)
- **Telegram Bot Integration**: Bot for status queries and critical alerts

## Architecture

```
┌─────────────────┐     ┌──────────────────┐     ┌─────────────────┐
│ Telegram Client │────▶│ Proxy Manager    │────▶│ Upstream Proxy  │
│   (SOCKS5/      │     │  - Health Check  │     │   (SOCKS5/      │
│    MTProto)     │     │  - Load Balancer │     │    MTProto)     │
└─────────────────┘     │  - Metrics       │     └─────────────────┘
                        │  - Bot Alerts    │
                        └──────────────────┘
                                 │
                                 ▼
                        ┌──────────────────┐
                        │   Prometheus     │
                        │   / Grafana      │
                        └──────────────────┘
```

## Project Structure

```
proxy-switcher/
├── cmd/
│   └── main.go              # Application entry point
├── internal/
│   ├── config/              # Configuration management
│   ├── metrics/             # Prometheus metrics collector
│   ├── healthcheck/         # Upstream health checking
│   ├── proxy/               # SOCKS5 and MTProto proxy servers
│   ├── router/              # Traffic routing logic
│   └── bot/                 # Telegram bot integration
├── configs/
│   └── config.example.yaml  # Example configuration
├── deploy/
│   ├── prometheus.yml       # Prometheus configuration
│   └── grafana/             # Grafana dashboards
├── Dockerfile
├── docker-compose.yml
└── README.md
```

## Quick Start

### Using Docker Compose

1. Clone the repository:
```bash
git clone https://github.com/Nakray/proxy-switcher.git
cd proxy-switcher
```

2. Copy and edit the configuration:
```bash
cp configs/config.example.yaml configs/config.yaml
# Edit configs/config.yaml with your upstream proxies
```

3. Start the service:
```bash
docker-compose up -d
```

4. With monitoring stack (Prometheus + Grafana):
```bash
docker-compose --profile monitoring up -d
```

### Manual Build

```bash
# Build
go build -o proxy-switcher ./cmd/

# Run with config file
./proxy-switcher -config configs/config.yaml

# Or run with environment variables
export PROXY_SOCKS5_PORT=1080
export PROXY_MTProto_PORT=2080
export BOT_TOKEN="your-bot-token"
./proxy-switcher
```

## Configuration

### Data Storage

The service uses **SQLite** to store upstream configuration. All changes made via the Telegram bot are persisted and survive restarts.

- **Default DB path**: `data/proxy-switcher.db`
- **CLI flag**: `-db path/to/database.db`
- **Initialization**: Database is created automatically on first run
- **Seed data**: Upstreams from config are loaded only if DB is empty

### Configuration Methods

**Environment variables are NOT used.** All configuration is done via YAML file or CLI flags.

| Flag | Description | Default |
|------|-------------|---------|
| `-config` | Path to YAML config file | - |
| `-db` | Path to SQLite database | `data/proxy-switcher.db` |

### YAML Configuration

See `configs/config.example.yaml` for a complete example.

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `PROXY_SOCKS5_PORT` | SOCKS5 listener port | 1080 |
| `PROXY_MTProto_PORT` | MTProto listener port | 2080 |
| `PROXY_ENABLED` | Enable proxy listeners | true |
| `HEALTH_CHECK_INTERVAL` | Health check interval | 10s |
| `HEALTH_CHECK_TIMEOUT` | Health check timeout | 5s |
| `METRICS_PORT` | Prometheus metrics port | 9090 |
| `METRICS_ENABLED` | Enable metrics | true |
| `BOT_TOKEN` | Telegram bot token | - |
| `BOT_ADMIN_CHAT_IDS` | Admin chat IDs (JSON array) | - |
| `LOG_LEVEL` | Logging level | info |

## Telegram Bot Commands

### Status Commands
- `/start` or `/help` - Show help message
- `/status` - Show current proxy status
- `/upstreams` - List all upstreams with health status
- `/metrics` - Show metrics summary

### Management Commands
- `/manage` - Open interactive management menu
- `/add <name> <type> <host> <port> [username] [password]` - Add new upstream
- `/remove <name>` - Remove upstream
- `/enable <name>` - Enable upstream
- `/disable <name>` - Disable upstream

**Examples:**
```
/add myproxy socks5 proxy.example.com 1080 user pass
/add mtproxy mtproto mt.example.com 443
/enable myproxy
/disable myproxy
/remove myproxy
```

### Interactive Management

The `/manage` command opens an interactive menu with inline buttons:
- ⏸️/▶️ - Disable/Enable upstream
- 🗑️ - Remove upstream (with confirmation)
- 🔄 - Refresh status
- ➕ - Add new upstream

## Metrics

The service exposes Prometheus metrics on port 9090:

| Metric | Description |
|--------|-------------|
| `proxy_active_connections` | Current active connections |
| `proxy_total_connections` | Total connections since start |
| `proxy_connection_duration_seconds` | Connection duration histogram |
| `proxy_bytes_transferred_total` | Total bytes transferred |
| `upstream_latency_milliseconds` | Upstream latency by name/type |
| `upstream_health_status` | Upstream health (1=healthy, 0=unhealthy) |
| `upstream_requests_total` | Requests forwarded to upstream |
| `upstream_failures_total` | Upstream connection failures |
| `upstream_reconnects_total` | Upstream reconnection attempts |
| `health_check_duration_seconds` | Health check duration |
| `health_check_errors_total` | Health check errors |
| `bot_messages_sent_total` | Messages sent by bot |
| `bot_commands_total` | Bot commands received |

### Grafana Dashboard

Access Grafana at `http://localhost:3000` (default credentials: admin/admin).

The pre-configured dashboard includes:
- Active/Total connections
- Bytes transferred
- Upstream health status
- Latency graphs
- Request rates
- Failure rates

## API Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /metrics` | Prometheus metrics |
| `GET /health` | Health check endpoint |

## Upstream Configuration

### SOCKS5 Upstream

```yaml
upstreams:
  - name: "my-socks5-proxy"
    type: "socks5"
    host: "proxy.example.com"
    port: 1080
    username: "user"      # Optional
    password: "pass"      # Optional
```

### MTProto Upstream

```yaml
upstreams:
  - name: "my-mtproto-proxy"
    type: "mtproto"
    host: "mtproxy.example.com"
    port: 443
    secret: "dd00000000000000000000000000000000"  # Optional
```

## Development

### Running Tests

```bash
go test ./...
```

### Building Docker Image

```bash
docker build -t proxy-switcher .
```

## Production Deployment

### Recommended VPS Specifications

- CPU: 1+ cores
- RAM: 512MB+ 
- Storage: 1GB+
- Network: 100Mbps+

### Security Considerations

1. **Firewall**: Only expose necessary ports (1080, 2080, 9090)
2. **TLS**: Consider putting behind a reverse proxy with TLS
3. **Authentication**: Use SOCKS5 authentication for client access
4. **Secrets**: Never commit secrets to version control
5. **Updates**: Keep the service updated for security patches

### Monitoring Setup

1. Configure Prometheus to scrape metrics
2. Import Grafana dashboard from `deploy/grafana/`
3. Set up alerts for:
   - All upstreams down
   - High failure rate
   - High latency

## Troubleshooting

### All Upstreams Down

1. Check network connectivity to upstreams
2. Verify upstream proxy credentials
3. Check health check logs: `docker-compose logs proxy-switcher`

### High Latency

1. Review upstream latency metrics
2. Consider adding geographically closer upstreams
3. Adjust health check interval for faster detection

### Connection Issues

1. Verify firewall rules allow incoming connections
2. Check if ports are not in use: `netstat -tlnp`
3. Review application logs for errors

## License

MIT License - see LICENSE file for details.

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## Support

For issues and feature requests, please use GitHub Issues.
