package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Upstream represents an upstream proxy configuration
type Upstream struct {
	Name     string `yaml:"name" json:"name"`
	Host     string `yaml:"host" json:"host"`
	Port     int    `yaml:"port" json:"port"`
	Username string `yaml:"username,omitempty" json:"username,omitempty"`
	Password string `yaml:"password,omitempty" json:"password,omitempty"`
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
	SOCKS5Port int  `yaml:"socks5_port" json:"socks5_port"`
	Enabled    bool `yaml:"enabled" json:"enabled"`
}

// Config holds the entire application configuration
type Config struct {
	Proxy             ProxyConfig       `yaml:"proxy" json:"proxy"`
	Upstreams         []Upstream        `yaml:"upstreams" json:"upstreams"`
	BootstrapUpstream *Upstream         `yaml:"bootstrap_upstream,omitempty" json:"bootstrap_upstream,omitempty"`
	HealthCheck       HealthCheckConfig `yaml:"health_check" json:"health_check"`
	Bot               BotConfig         `yaml:"bot" json:"bot"`
	Metrics           MetricsConfig     `yaml:"metrics" json:"metrics"`
	LogLevel          string            `yaml:"log_level" json:"log_level"`
}

// DefaultConfig returns a configuration with default values
func DefaultConfig() *Config {
	return &Config{
		Proxy: ProxyConfig{
			SOCKS5Port: 1080,
			Enabled:    true,
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

	return config, nil
}

// Validate validates the configuration
func (c *Config) Validate() error {
	if c.Proxy.Enabled && c.Proxy.SOCKS5Port == 0 {
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
	}
	safeConfig.Bot.Token = "***"

	data, _ := yaml.Marshal(&safeConfig)
	return string(data)
}
