package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Proxy.SOCKS5Port != 1080 {
		t.Errorf("Expected SOCKS5Port 1080, got %d", cfg.Proxy.SOCKS5Port)
	}
	if cfg.Proxy.MTProtoPort != 2080 {
		t.Errorf("Expected MTProtoPort 2080, got %d", cfg.Proxy.MTProtoPort)
	}
	if cfg.HealthCheck.Interval != 10*time.Second {
		t.Errorf("Expected HealthCheck.Interval 10s, got %v", cfg.HealthCheck.Interval)
	}
	if cfg.Metrics.Port != 9090 {
		t.Errorf("Expected Metrics.Port 9090, got %d", cfg.Metrics.Port)
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name: "valid config",
			config: &Config{
				Proxy: ProxyConfig{
					SOCKS5Port: 1080,
					Enabled:    true,
				},
				Upstreams: []Upstream{
					{
						Name: "test-upstream",
						Type: UpstreamTypeSOCKS5,
						Host: "localhost",
						Port: 1081,
					},
				},
			},
			wantErr: false,
		},
		{
			name: "no upstreams",
			config: &Config{
				Proxy: ProxyConfig{
					SOCKS5Port: 1080,
					Enabled:    true,
				},
				Upstreams: []Upstream{},
			},
			wantErr: false,
		},
		{
			name: "bot enabled without token",
			config: &Config{
				Proxy: ProxyConfig{
					SOCKS5Port: 1080,
					Enabled:    true,
				},
				Bot: BotConfig{},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Config.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
proxy:
  socks5_port: 1080
  mtproto_port: 2080
  enabled: true

upstreams:
  - name: "upstream1"
    type: "socks5"
    host: "proxy1.example.com"
    port: 1080
    username: "user1"
    password: "pass1"

health_check:
  interval: 15s
  timeout: 5s
  max_retries: 3
  unhealthy_threshold: 3

bot:
  enabled: false
  token: "test-token"
  admin_chat_ids: [123456789]
  alert_interval: 5m

metrics:
  enabled: true
  port: 9090
  path: "/metrics"

log_level: "debug"
`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write temp config: %v", err)
	}

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatalf("LoadFromFile() error = %v", err)
	}

	if cfg.Proxy.SOCKS5Port != 1080 {
		t.Errorf("Expected SOCKS5Port 1080, got %d", cfg.Proxy.SOCKS5Port)
	}
	if cfg.Proxy.MTProtoPort != 2080 {
		t.Errorf("Expected MTProtoPort 2080, got %d", cfg.Proxy.MTProtoPort)
	}
	if len(cfg.Upstreams) != 1 {
		t.Errorf("Expected 1 upstream, got %d", len(cfg.Upstreams))
	}
	if cfg.Upstreams[0].Name != "upstream1" {
		t.Errorf("Expected upstream name 'upstream1', got %s", cfg.Upstreams[0].Name)
	}
	if cfg.HealthCheck.Interval != 15*time.Second {
		t.Errorf("Expected HealthCheck.Interval 15s, got %v", cfg.HealthCheck.Interval)
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("PROXY_SOCKS5_PORT", "2000")
	t.Setenv("PROXY_MTProto_PORT", "3000")
	t.Setenv("HEALTH_CHECK_INTERVAL", "20s")
	t.Setenv("METRICS_PORT", "8080")

	cfg := LoadFromEnv()

	if cfg.Proxy.SOCKS5Port != 2000 {
		t.Errorf("Expected SOCKS5Port 2000 from env, got %d", cfg.Proxy.SOCKS5Port)
	}
	if cfg.Proxy.MTProtoPort != 3000 {
		t.Errorf("Expected MTProtoPort 3000 from env, got %d", cfg.Proxy.MTProtoPort)
	}
	if cfg.HealthCheck.Interval != 20*time.Second {
		t.Errorf("Expected HealthCheck.Interval 20s from env, got %v", cfg.HealthCheck.Interval)
	}
	if cfg.Metrics.Port != 8080 {
		t.Errorf("Expected Metrics.Port 8080 from env, got %d", cfg.Metrics.Port)
	}
}

func TestConfigString(t *testing.T) {
	cfg := &Config{
		Proxy: ProxyConfig{
			SOCKS5Port: 1080,
			Enabled:    true,
		},
		Upstreams: []Upstream{
			{
				Name:     "test",
				Type:     UpstreamTypeSOCKS5,
				Host:     "localhost",
				Port:     1081,
				Password: "mysecretpassword",
				Secret:   "mtproto-super-secret",
			},
		},
		Bot: BotConfig{
			Token: "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11",
		},
	}

	str := cfg.String()

	if contains(str, "mysecretpassword") {
		t.Error("Config.String() should mask passwords")
	}
	if contains(str, "mtproto-super-secret") {
		t.Error("Config.String() should mask MTProto secrets")
	}
	if contains(str, "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11") {
		t.Error("Config.String() should mask bot token")
	}

	if !contains(str, "***") {
		t.Error("Config.String() should show masked values as ***")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
