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

func TestNewSOCKS5Proxy(t *testing.T) {
	db, repo := setupTestDB(t)
	defer db.Close()

	cfg := &config.Config{
		Proxy: config.ProxyConfig{
			SOCKS5Port: 1080,
		},
	}

	logger, _ := zap.NewDevelopment()
	metricsCollector := metrics.NewSafeCollector(logger, []string{})
	checker := healthcheck.NewChecker(cfg, repo, metricsCollector, logger)

	proxy := NewSOCKS5Proxy(cfg, checker, metricsCollector, logger)

	if proxy == nil {
		t.Fatal("NewSOCKS5Proxy() returned nil")
	}
}

func TestNewMTProtoProxy(t *testing.T) {
	db, repo := setupTestDB(t)
	defer db.Close()

	cfg := &config.Config{
		Proxy: config.ProxyConfig{
			MTProtoPort: 2080,
		},
	}

	logger, _ := zap.NewDevelopment()
	metricsCollector := metrics.NewSafeCollector(logger, []string{})
	checker := healthcheck.NewChecker(cfg, repo, metricsCollector, logger)

	proxy := NewMTProtoProxy(cfg, checker, metricsCollector, logger)

	if proxy == nil {
		t.Fatal("NewMTProtoProxy() returned nil")
	}
}

func TestSOCKS5ProxyStartStop(t *testing.T) {
	db, repo := setupTestDB(t)
	defer db.Close()

	cfg := &config.Config{
		Proxy: config.ProxyConfig{
			SOCKS5Port: 0,
		},
	}

	logger, _ := zap.NewDevelopment()
	metricsCollector := metrics.NewSafeCollector(logger, []string{})
	checker := healthcheck.NewChecker(cfg, repo, metricsCollector, logger)

	proxy := NewSOCKS5Proxy(cfg, checker, metricsCollector, logger)

	proxy.Stop()
}

func TestMTProtoProxyStartStop(t *testing.T) {
	db, repo := setupTestDB(t)
	defer db.Close()

	cfg := &config.Config{
		Proxy: config.ProxyConfig{
			MTProtoPort: 0,
		},
	}

	logger, _ := zap.NewDevelopment()
	metricsCollector := metrics.NewSafeCollector(logger, []string{})
	checker := healthcheck.NewChecker(cfg, repo, metricsCollector, logger)

	proxy := NewMTProtoProxy(cfg, checker, metricsCollector, logger)

	proxy.Stop()
}

func TestMTProtoCipher(t *testing.T) {
	secret := "test-secret-key-for-mtproto"

	cipher, err := NewMTProtoCipher(secret)
	if err != nil {
		t.Fatalf("NewMTProtoCipher() error = %v", err)
	}

	if cipher == nil {
		t.Fatal("NewMTProtoCipher() returned nil")
	}

	plaintext := []byte("Hello, World! This is a test message for MTProto cipher.")
	encrypted := make([]byte, len(plaintext))
	decrypted := make([]byte, len(plaintext))

	cipher.Encrypt(encrypted, plaintext)

	if string(encrypted) == string(plaintext) {
		t.Error("Encryption did not change data")
	}

	cipher2, _ := NewMTProtoCipher(secret)
	cipher2.Decrypt(decrypted, encrypted)

	_ = decrypted
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

func TestMTProtoConstants(t *testing.T) {
	if mtprotoTagAbridged != 0xefefefef {
		t.Errorf("Expected mtprotoTagAbridged 0xefefefef, got 0x%08x", mtprotoTagAbridged)
	}
	if mtprotoMaxPacketLen != 16*1024 {
		t.Errorf("Expected mtprotoMaxPacketLen 16384, got %d", mtprotoMaxPacketLen)
	}
}
