package proxy

import (
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"

	"github.com/Nakray/proxy-switcher/internal/config"
	"github.com/Nakray/proxy-switcher/internal/database"
	"github.com/Nakray/proxy-switcher/internal/healthcheck"
	"github.com/Nakray/proxy-switcher/internal/metrics"
)

func setupTestDB(t *testing.T) (*database.Database, *database.UpstreamRepository) {
	t.Helper()

	tmpFile := t.TempDir() + "/test.db"
	logger, _ := zap.NewDevelopment()

	db, err := database.NewDatabase(tmpFile, logger)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	return db, database.NewUpstreamRepository(db)
}

func TestSOCKS5ProxyStartStop(t *testing.T) {
	db, repo := setupTestDB(t)
	defer db.Close()

	cfg := &config.Config{
		Proxy: config.ProxyConfig{
			SOCKS5Port: 0,
			Enabled:    true,
		},
	}

	logger, _ := zap.NewDevelopment()
	metricsCollector := metrics.NewSafeCollector(logger, []string{})
	checker := healthcheck.NewChecker(cfg, repo, metricsCollector, logger)

	proxy := NewSOCKS5Proxy(cfg, checker, metricsCollector, logger)

	proxy.Stop()
}

func TestSOCKS5Constants(t *testing.T) {
	if socks5Version != 0x05 {
		t.Errorf("Expected socks5Version 0x05, got 0x%02x", socks5Version)
	}
	if socks5AuthNone != 0x00 {
		t.Errorf("Expected socks5AuthNone 0x00, got 0x%02x", socks5AuthNone)
	}
	if socks5CmdConnect != 0x01 {
		t.Errorf("Expected socks5CmdConnect 0x01, got 0x%02x", socks5CmdConnect)
	}
}
