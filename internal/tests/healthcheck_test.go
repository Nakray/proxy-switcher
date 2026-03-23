package tests

import (
	"context"
	"testing"
	"time"

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

func TestNewChecker(t *testing.T) {
	db, repo := setupTestDB(t)
	defer db.Close()

	cfg := &config.Config{
		Upstreams: []config.Upstream{},
		HealthCheck: config.HealthCheckConfig{
			Interval: 10 * time.Second,
			Timeout:  5 * time.Second,
		},
	}

	logger, _ := zap.NewDevelopment()
	metricsCollector := metrics.NewSafeCollector(logger, []string{})

	checker := healthcheck.NewChecker(cfg, repo, metricsCollector, logger)

	if checker == nil {
		t.Fatal("NewChecker() returned nil")
	}

	// Load upstreams (empty in this case)
	if err := checker.LoadUpstreams(); err != nil {
		t.Fatalf("LoadUpstreams() error = %v", err)
	}

	statuses := checker.GetAllStatuses()
	if len(statuses) != 0 {
		t.Errorf("Expected 0 statuses, got %d", len(statuses))
	}
}

func TestGetHealthyUpstreams(t *testing.T) {
	db, repo := setupTestDB(t)
	defer db.Close()

	cfg := &config.Config{
		Upstreams: []config.Upstream{},
		HealthCheck: config.HealthCheckConfig{
			Interval: 10 * time.Second,
			Timeout:  5 * time.Second,
		},
	}

	logger, _ := zap.NewDevelopment()
	metricsCollector := metrics.NewSafeCollector(logger, []string{})
	checker := healthcheck.NewChecker(cfg, repo, metricsCollector, logger)

	if err := checker.LoadUpstreams(); err != nil {
		t.Fatalf("LoadUpstreams() error = %v", err)
	}

	// Initially all upstreams should be unhealthy (status -1)
	healthy := checker.GetHealthyUpstreams()
	if len(healthy) != 0 {
		t.Errorf("Expected 0 healthy upstreams initially, got %d", len(healthy))
	}
}

func TestGetBestUpstream(t *testing.T) {
	db, repo := setupTestDB(t)
	defer db.Close()

	cfg := &config.Config{
		Upstreams: []config.Upstream{},
		HealthCheck: config.HealthCheckConfig{
			Interval: 10 * time.Second,
			Timeout:  5 * time.Second,
		},
	}

	logger, _ := zap.NewDevelopment()
	metricsCollector := metrics.NewSafeCollector(logger, []string{})
	checker := healthcheck.NewChecker(cfg, repo, metricsCollector, logger)

	if err := checker.LoadUpstreams(); err != nil {
		t.Fatalf("LoadUpstreams() error = %v", err)
	}

	// Initially no healthy upstream
	best := checker.GetBestUpstream(config.UpstreamTypeSOCKS5)
	if best != nil {
		t.Errorf("Expected nil best upstream initially, got %v", best)
	}
}

func TestAreAllUpstreamsDown(t *testing.T) {
	db, repo := setupTestDB(t)
	defer db.Close()

	cfg := &config.Config{
		Upstreams: []config.Upstream{},
		HealthCheck: config.HealthCheckConfig{
			Interval: 10 * time.Second,
			Timeout:  5 * time.Second,
		},
	}

	logger, _ := zap.NewDevelopment()
	metricsCollector := metrics.NewSafeCollector(logger, []string{})
	checker := healthcheck.NewChecker(cfg, repo, metricsCollector, logger)

	if err := checker.LoadUpstreams(); err != nil {
		t.Fatalf("LoadUpstreams() error = %v", err)
	}

	// Initially all should be considered down (unknown status)
	if !checker.AreAllUpstreamsDown() {
		t.Error("Expected all upstreams down initially")
	}
}

func TestGetHealthyCount(t *testing.T) {
	db, repo := setupTestDB(t)
	defer db.Close()

	cfg := &config.Config{
		Upstreams: []config.Upstream{},
		HealthCheck: config.HealthCheckConfig{
			Interval: 10 * time.Second,
			Timeout:  5 * time.Second,
		},
	}

	logger, _ := zap.NewDevelopment()
	metricsCollector := metrics.NewSafeCollector(logger, []string{})
	checker := healthcheck.NewChecker(cfg, repo, metricsCollector, logger)

	if err := checker.LoadUpstreams(); err != nil {
		t.Fatalf("LoadUpstreams() error = %v", err)
	}

	count := checker.GetHealthyCount()
	if count != 0 {
		t.Errorf("Expected 0 healthy count initially, got %d", count)
	}
}

func TestCheckerStartStop(t *testing.T) {
	db, repo := setupTestDB(t)
	defer db.Close()

	cfg := &config.Config{
		Upstreams: []config.Upstream{},
		HealthCheck: config.HealthCheckConfig{
			Interval: 100 * time.Millisecond,
			Timeout:  50 * time.Millisecond,
		},
	}

	logger, _ := zap.NewDevelopment()
	metricsCollector := metrics.NewSafeCollector(logger, []string{})
	checker := healthcheck.NewChecker(cfg, repo, metricsCollector, logger)

	if err := checker.LoadUpstreams(); err != nil {
		t.Fatalf("LoadUpstreams() error = %v", err)
	}

	checker.Start()
	time.Sleep(150 * time.Millisecond)
	checker.Stop()

	// Test passes if no panic occurs
}

func TestProbeSOCKS5(t *testing.T) {
	db, repo := setupTestDB(t)
	defer db.Close()

	cfg := &config.Config{
		Upstreams: []config.Upstream{},
		HealthCheck: config.HealthCheckConfig{
			Interval: 10 * time.Second,
			Timeout:  100 * time.Millisecond,
		},
	}

	logger, _ := zap.NewDevelopment()
	metricsCollector := metrics.NewSafeCollector(logger, []string{})
	checker := healthcheck.NewChecker(cfg, repo, metricsCollector, logger)

	if err := checker.LoadUpstreams(); err != nil {
		t.Fatalf("LoadUpstreams() error = %v", err)
	}

	// Test with non-existent server (should fail)
	upstream := config.Upstream{
		Name: "test1",
		Type: config.UpstreamTypeSOCKS5,
		Host: "localhost",
		Port: 1080,
	}
	healthy, latency := checker.ProbeSOCKS5(context.Background(), upstream)
	if healthy {
		t.Error("Expected probe to fail for non-existent server")
	}
	if latency != 0 {
		t.Errorf("Expected 0 latency on failure, got %v", latency)
	}
}

func TestProbeMTProto(t *testing.T) {
	db, repo := setupTestDB(t)
	defer db.Close()

	cfg := &config.Config{
		Upstreams: []config.Upstream{},
		HealthCheck: config.HealthCheckConfig{
			Interval: 10 * time.Second,
			Timeout:  100 * time.Millisecond,
		},
	}

	logger, _ := zap.NewDevelopment()
	metricsCollector := metrics.NewSafeCollector(logger, []string{})
	checker := healthcheck.NewChecker(cfg, repo, metricsCollector, logger)

	if err := checker.LoadUpstreams(); err != nil {
		t.Fatalf("LoadUpstreams() error = %v", err)
	}

	// Test with non-existent server (should fail)
	upstream := config.Upstream{
		Name: "test1",
		Type: config.UpstreamTypeMTProto,
		Host: "localhost",
		Port: 2080,
	}
	healthy, latency := checker.ProbeMTProto(context.Background(), upstream)
	if healthy {
		t.Error("Expected probe to fail for non-existent server")
	}
	if latency != 0 {
		t.Errorf("Expected 0 latency on failure, got %v", latency)
	}
}

func TestAddUpstream(t *testing.T) {
	db, repo := setupTestDB(t)
	defer db.Close()

	cfg := &config.Config{
		Upstreams: []config.Upstream{},
		HealthCheck: config.HealthCheckConfig{
			Interval: 10 * time.Second,
			Timeout:  5 * time.Second,
		},
	}

	logger, _ := zap.NewDevelopment()
	metricsCollector := metrics.NewSafeCollector(logger, []string{})
	checker := healthcheck.NewChecker(cfg, repo, metricsCollector, logger)

	if err := checker.LoadUpstreams(); err != nil {
		t.Fatalf("LoadUpstreams() error = %v", err)
	}

	upstream := config.Upstream{
		Name:    "new-upstream",
		Type:    config.UpstreamTypeSOCKS5,
		Host:    "localhost",
		Port:    1080,
		Enabled: true,
	}

	err := checker.AddUpstream(upstream)
	if err != nil {
		t.Fatalf("AddUpstream() error = %v", err)
	}

	names := checker.GetUpstreamNames()
	if len(names) != 1 {
		t.Errorf("Expected 1 upstream, got %d", len(names))
	}

	// Try to add duplicate
	err = checker.AddUpstream(upstream)
	if err == nil {
		t.Error("Expected error when adding duplicate upstream")
	}
}

func TestRemoveUpstream(t *testing.T) {
	db, repo := setupTestDB(t)
	defer db.Close()

	// Add upstream to database first
	upstream := config.Upstream{
		Name:    "test1",
		Type:    config.UpstreamTypeSOCKS5,
		Host:    "localhost",
		Port:    1080,
		Enabled: true,
	}
	if err := repo.Create(upstream); err != nil {
		t.Fatalf("Failed to create upstream: %v", err)
	}

	cfg := &config.Config{
		Upstreams: []config.Upstream{},
		HealthCheck: config.HealthCheckConfig{
			Interval: 10 * time.Second,
			Timeout:  5 * time.Second,
		},
	}

	logger, _ := zap.NewDevelopment()
	metricsCollector := metrics.NewSafeCollector(logger, []string{"test1"})
	checker := healthcheck.NewChecker(cfg, repo, metricsCollector, logger)

	if err := checker.LoadUpstreams(); err != nil {
		t.Fatalf("LoadUpstreams() error = %v", err)
	}

	err := checker.RemoveUpstream("test1")
	if err != nil {
		t.Fatalf("RemoveUpstream() error = %v", err)
	}

	names := checker.GetUpstreamNames()
	if len(names) != 0 {
		t.Errorf("Expected 0 upstreams after removal, got %d", len(names))
	}

	// Try to remove non-existent
	err = checker.RemoveUpstream("non-existent")
	if err == nil {
		t.Error("Expected error when removing non-existent upstream")
	}
}

func TestEnableDisableUpstream(t *testing.T) {
	db, repo := setupTestDB(t)
	defer db.Close()

	// Add upstream to database first
	upstream := config.Upstream{
		Name:    "test1",
		Type:    config.UpstreamTypeSOCKS5,
		Host:    "localhost",
		Port:    1080,
		Enabled: true,
	}
	if err := repo.Create(upstream); err != nil {
		t.Fatalf("Failed to create upstream: %v", err)
	}

	cfg := &config.Config{
		Upstreams: []config.Upstream{},
		HealthCheck: config.HealthCheckConfig{
			Interval: 10 * time.Second,
			Timeout:  5 * time.Second,
		},
	}

	logger, _ := zap.NewDevelopment()
	metricsCollector := metrics.NewSafeCollector(logger, []string{"test1"})
	checker := healthcheck.NewChecker(cfg, repo, metricsCollector, logger)

	if err := checker.LoadUpstreams(); err != nil {
		t.Fatalf("LoadUpstreams() error = %v", err)
	}

	// Disable
	err := checker.DisableUpstream("test1")
	if err != nil {
		t.Fatalf("DisableUpstream() error = %v", err)
	}

	upstream = *checker.GetUpstreamByName("test1")
	if upstream.Enabled {
		t.Error("Expected upstream to be disabled")
	}

	// Enable
	err = checker.EnableUpstream("test1")
	if err != nil {
		t.Fatalf("EnableUpstream() error = %v", err)
	}

	upstream = *checker.GetUpstreamByName("test1")
	if !upstream.Enabled {
		t.Error("Expected upstream to be enabled")
	}

	// Test non-existent
	err = checker.EnableUpstream("non-existent")
	if err == nil {
		t.Error("Expected error when enabling non-existent upstream")
	}
}

func TestGetBestUpstreamWithDisabled(t *testing.T) {
	db, repo := setupTestDB(t)
	defer db.Close()

	// Add upstreams to database
	upstreams := []config.Upstream{
		{
			Name:    "test1",
			Type:    config.UpstreamTypeSOCKS5,
			Host:    "localhost",
			Port:    1080,
			Enabled: true,
		},
		{
			Name:    "test2",
			Type:    config.UpstreamTypeSOCKS5,
			Host:    "localhost",
			Port:    1081,
			Enabled: false,
		},
	}
	for _, u := range upstreams {
		if err := repo.Create(u); err != nil {
			t.Fatalf("Failed to create upstream: %v", err)
		}
	}

	cfg := &config.Config{
		Upstreams: []config.Upstream{},
		HealthCheck: config.HealthCheckConfig{
			Interval: 10 * time.Second,
			Timeout:  5 * time.Second,
		},
	}

	logger, _ := zap.NewDevelopment()
	metricsCollector := metrics.NewSafeCollector(logger, []string{"test1", "test2"})
	checker := healthcheck.NewChecker(cfg, repo, metricsCollector, logger)

	if err := checker.LoadUpstreams(); err != nil {
		t.Fatalf("LoadUpstreams() error = %v", err)
	}

	// Manually set health status for testing
	checker.Mu.Lock()
	if status, ok := checker.Statuses["test1_socks5"]; ok {
		status.Healthy = true
		status.Latency = 10 * time.Millisecond
	}
	if status, ok := checker.Statuses["test2_socks5"]; ok {
		status.Healthy = true
		status.Latency = 5 * time.Millisecond
		status.Upstream.Enabled = false
	}
	checker.Mu.Unlock()

	// Should return test1 even though test2 has lower latency (because test2 is disabled)
	best := checker.GetBestUpstream(config.UpstreamTypeSOCKS5)
	if best == nil {
		t.Fatal("GetBestUpstream() returned nil")
	}
	if best.Name != "test1" {
		t.Errorf("Expected test1, got %s", best.Name)
	}
}
