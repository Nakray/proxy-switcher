package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// UpstreamType defines the type of upstream proxy
type UpstreamType string

const (
	UpstreamTypeSOCKS5  UpstreamType = "socks5"
	UpstreamTypeMTProto UpstreamType = "mtproto"
)

// Upstream represents an upstream proxy configuration
type Upstream struct {
	Name     string       `yaml:"name" json:"name"`
	Type     UpstreamType `yaml:"type" json:"type"`
	Host     string       `yaml:"host" json:"host"`
	Port     int          `yaml:"port" json:"port"`
	Username string       `yaml:"username,omitempty" json:"username,omitempty"`
	Password string       `yaml:"password,omitempty" json:"password,omitempty"`
	// MTProto specific
	Secret string `yaml:"secret,omitempty" json:"secret,omitempty"`
	// Runtime status (not persisted in YAML)
	Enabled bool `yaml:"enabled" json:"enabled"`
}

// HealthCheckConfig holds health check configuration
type HealthCheckConfig struct {
	Interval           time.Duration `yaml:"interval" json:"interval"`
	Timeout            time.Duration `yaml:"timeout" json:"timeout"`
	MaxRetries         int           `yaml:"max_retries" json:"max_retries"`
	UnhealthyThreshold int           `yaml:"unhealthy_threshold" json:"unhealthy_threshold"`
}

// BotConfig holds Telegram bot configuration
type BotConfig struct {
	Token         string        `yaml:"token" json:"token"`
	AdminChatIDs  []int64       `yaml:"admin_chat_ids" json:"admin_chat_ids"`
	AlertInterval time.Duration `yaml:"alert_interval" json:"alert_interval"`
	UseProxy      bool          `yaml:"use_proxy,omitempty" json:"use_proxy,omitempty"`
	MaxRetries    int           `yaml:"max_retries,omitempty" json:"max_retries,omitempty"`
	RetryDelay    time.Duration `yaml:"retry_delay,omitempty" json:"retry_delay,omitempty"`
}

// MetricsConfig holds Prometheus metrics configuration
type MetricsConfig struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Port    int    `yaml:"port" json:"port"`
	Path    string `yaml:"path" json:"path"`
}

// ProxyConfig holds the main proxy listener configuration
type ProxyConfig struct {
	SOCKS5Port  int  `yaml:"socks5_port" json:"socks5_port"`
	MTProtoPort int  `yaml:"mtproto_port" json:"mtproto_port"`
	Enabled     bool `yaml:"enabled" json:"enabled"`
}

// Config holds the entire application configuration
type Config struct {
	Proxy           ProxyConfig       `yaml:"proxy" json:"proxy"`
	Upstreams       []Upstream        `yaml:"upstreams" json:"upstreams"`
	BootstrapUpstream *Upstream       `yaml:"bootstrap_upstream,omitempty" json:"bootstrap_upstream,omitempty"`
	HealthCheck     HealthCheckConfig `yaml:"health_check" json:"health_check"`
	Bot             BotConfig         `yaml:"bot" json:"bot"`
	Metrics         MetricsConfig     `yaml:"metrics" json:"metrics"`
	LogLevel        string            `yaml:"log_level" json:"log_level"`
}

// DefaultConfig returns a configuration with default values
func DefaultConfig() *Config {
	return &Config{
		Proxy: ProxyConfig{
			SOCKS5Port:  1080,
			MTProtoPort: 2080,
			Enabled:     true,
		},
		HealthCheck: HealthCheckConfig{
			Interval:           10 * time.Second,
			Timeout:            5 * time.Second,
			MaxRetries:         3,
			UnhealthyThreshold: 3,
		},
		Bot: BotConfig{
			AlertInterval: 5 * time.Minute,
		},
		Metrics: MetricsConfig{
			Enabled: true,
			Port:    9090,
			Path:    "/metrics",
		},
		LogLevel: "info",
	}
}

// LoadFromFile loads configuration from a YAML file
func LoadFromFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	config := DefaultConfig()
	if err := yaml.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Apply environment variable overrides
	applyEnvOverrides(config)

	return config, nil
}

func applyEnvOverrides(config *Config) {
	// Proxy settings
	if val := os.Getenv("PROXY_SOCKS5_PORT"); val != "" {
		if port := parseInt(val); port > 0 {
			config.Proxy.SOCKS5Port = port
		}
	}
	if val := os.Getenv("PROXY_MTProto_PORT"); val != "" {
		if port := parseInt(val); port > 0 {
			config.Proxy.MTProtoPort = port
		}
	}
	if val := os.Getenv("PROXY_ENABLED"); val != "" {
		config.Proxy.Enabled = parseBool(val)
	}

	// Health check settings
	if val := os.Getenv("HEALTH_CHECK_INTERVAL"); val != "" {
		if d := parseDuration(val); d > 0 {
			config.HealthCheck.Interval = d
		}
	}
	if val := os.Getenv("HEALTH_CHECK_TIMEOUT"); val != "" {
		if d := parseDuration(val); d > 0 {
			config.HealthCheck.Timeout = d
		}
	}

	// Bot settings
	if val := os.Getenv("BOT_TOKEN"); val != "" {
		config.Bot.Token = val
	}
	if val := os.Getenv("BOT_ADMIN_CHAT_IDS"); val != "" {
		config.Bot.AdminChatIDs = parseIntSlice(val)
	}

	// Metrics settings
	if val := os.Getenv("METRICS_PORT"); val != "" {
		if port := parseInt(val); port > 0 {
			config.Metrics.Port = port
		}
	}
	if val := os.Getenv("LOG_LEVEL"); val != "" {
		config.LogLevel = val
	}
}

func parseInt(s string) int {
	var v int
	fmt.Sscanf(s, "%d", &v)
	return v
}

func parseIntSlice(s string) []int64 {
	var result []int64
	var ids []json.Number

	// Try to parse as JSON array first
	if err := json.Unmarshal([]byte(s), &ids); err == nil {
		for _, id := range ids {
			if v, err := id.Int64(); err == nil {
				result = append(result, v)
			}
		}
		return result
	}

	// Fallback: comma-separated values
	fmt.Sscanf(s, "%d", &result)
	return result
}

func parseBool(s string) bool {
	return s == "true" || s == "1" || s == "yes"
}

func parseDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
}

// Validate validates the configuration
func (c *Config) Validate() error {
	if c.Proxy.Enabled && c.Proxy.SOCKS5Port == 0 && c.Proxy.MTProtoPort == 0 {
		return fmt.Errorf("at least one proxy port must be specified")
	}

	if c.Bot.Token == "" {
		return fmt.Errorf("bot token is required when bot is enabled")
	}

	// Upstreams are now loaded from database, not from config
	// Validation of upstreams happens at runtime when they are added via bot

	return nil
}

// String returns a string representation of the config (without secrets)
func (c *Config) String() string {
	safeConfig := *c
	for i := range safeConfig.Upstreams {
		safeConfig.Upstreams[i].Password = "***"
		safeConfig.Upstreams[i].Secret = "***"
	}
	safeConfig.Bot.Token = "***"

	data, _ := yaml.Marshal(&safeConfig)
	return string(data)
}
