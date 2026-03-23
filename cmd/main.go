package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/Nakray/proxy-switcher/internal/bot"
	"github.com/Nakray/proxy-switcher/internal/config"
	"github.com/Nakray/proxy-switcher/internal/database"
	"github.com/Nakray/proxy-switcher/internal/healthcheck"
	"github.com/Nakray/proxy-switcher/internal/metrics"
	"github.com/Nakray/proxy-switcher/internal/proxy"
)

func main() {
	// Parse command line flags
	configPath := flag.String("config", "", "Path to configuration file (YAML)")
	dbPath := flag.String("db", "data/proxy-switcher.db", "Path to SQLite database")
	flag.Parse()

	// Load configuration
	var cfg *config.Config
	var err error

	if *configPath != "" {
		cfg, err = config.LoadFromFile(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
			os.Exit(1)
		}
	} else {
		cfg = config.LoadFromEnv()
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid configuration: %v\n", err)
		os.Exit(1)
	}

	// Setup logger
	logger := setupLogger(cfg.LogLevel)
	defer logger.Sync()

	logger.Info("Starting Proxy Manager",
		zap.String("version", "1.0.0"),
		zap.String("config_file", *configPath))

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize database
	db, err := database.NewDatabase(*dbPath, logger)
	if err != nil {
		logger.Error("Failed to initialize database", zap.Error(err))
		os.Exit(1)
	}
	defer db.Close()

	upstreamRepo := database.NewUpstreamRepository(db)

	// Seed database with config upstreams if empty
	if err := upstreamRepo.Seed(cfg.Upstreams); err != nil {
		logger.Error("Failed to seed database", zap.Error(err))
	}

	// Collect upstream names for metrics
	upstreamNames, err := getUpstreamNames(upstreamRepo)
	if err != nil {
		logger.Error("Failed to get upstream names", zap.Error(err))
		upstreamNames = []string{}
	}

	// Initialize metrics collector
	metricsCollector := metrics.NewSafeCollector(logger, upstreamNames)
	if cfg.Metrics.Enabled {
		if err := metricsCollector.StartServer(ctx, cfg.Metrics.Port, cfg.Metrics.Path); err != nil {
			logger.Error("Failed to start metrics server", zap.Error(err))
		}
	}

	// Initialize health checker
	healthChecker := healthcheck.NewChecker(cfg, upstreamRepo, metricsCollector, logger)

	// Load upstreams from database
	if err := healthChecker.LoadUpstreams(); err != nil {
		logger.Error("Failed to load upstreams", zap.Error(err))
		os.Exit(1)
	}

	healthChecker.Start()

	// Initialize Telegram bot
	telegramBot, err := bot.NewBot(cfg, healthChecker, metricsCollector, logger)
	if err != nil {
		logger.Error("Failed to initialize Telegram bot", zap.Error(err))
	}
	if telegramBot != nil {
		if err := telegramBot.Start(ctx); err != nil {
			logger.Error("Failed to start Telegram bot", zap.Error(err))
		}
	}

	// Initialize and start proxies
	var socks5Proxy *proxy.SOCKS5Proxy
	var mtprotoProxy *proxy.MTProtoProxy

	if cfg.Proxy.Enabled {
		if cfg.Proxy.SOCKS5Port > 0 {
			socks5Proxy = proxy.NewSOCKS5Proxy(cfg, healthChecker, metricsCollector, logger)
			go func() {
				if err := socks5Proxy.Start(ctx); err != nil {
					logger.Error("SOCKS5 proxy error", zap.Error(err))
				}
			}()
		}

		if cfg.Proxy.MTProtoPort > 0 {
			mtprotoProxy = proxy.NewMTProtoProxy(cfg, healthChecker, metricsCollector, logger)
			go func() {
				if err := mtprotoProxy.Start(ctx); err != nil {
					logger.Error("MTProto proxy error", zap.Error(err))
				}
			}()
		}
	}

	logger.Info("Proxy Manager started successfully")

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigChan
	logger.Info("Shutdown signal received", zap.String("signal", sig.String()))

	// Graceful shutdown
	logger.Info("Starting graceful shutdown...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	// Stop accepting new connections
	if socks5Proxy != nil {
		socks5Proxy.Stop()
	}
	if mtprotoProxy != nil {
		mtprotoProxy.Stop()
	}

	// Stop health checker
	healthChecker.Stop()

	// Stop Telegram bot
	if telegramBot != nil {
		if err := telegramBot.Stop(); err != nil {
			logger.Error("Error stopping Telegram bot", zap.Error(err))
		}
	}

	// Stop metrics server
	if err := metricsCollector.StopServer(shutdownCtx); err != nil {
		logger.Error("Error stopping metrics server", zap.Error(err))
	}

	logger.Info("Proxy Manager stopped")
}

func getUpstreamNames(repo *database.UpstreamRepository) ([]string, error) {
	upstreams, err := repo.List()
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(upstreams))
	for _, u := range upstreams {
		names = append(names, u.Name)
	}
	return names, nil
}

func setupLogger(level string) *zap.Logger {
	var logLevel zapcore.Level
	if err := logLevel.UnmarshalText([]byte(level)); err != nil {
		logLevel = zapcore.InfoLevel
	}

	config := zap.NewProductionConfig()
	config.Level = zap.NewAtomicLevelAt(logLevel)
	config.EncoderConfig.TimeKey = "timestamp"
	config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder

	logger, _ := config.Build()
	return logger
}
