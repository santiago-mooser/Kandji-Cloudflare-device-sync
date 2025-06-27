package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"cloudflare-kandji-device-sync/cloudflare"
	"cloudflare-kandji-device-sync/config"
	"cloudflare-kandji-device-sync/internal/ratelimit"
	"cloudflare-kandji-device-sync/kandji"
	"cloudflare-kandji-device-sync/syncer"
)

var (
	Version    = "dev"
	Commit     = "n/a"
	CommitDate = "n/a"
	TreeState  = "n/a"
)

func printVersion() {
	showVersion := flag.Bool("version", false, "show version")
	flag.Parse()
	if *showVersion {
		fmt.Printf("%s, %s, %s, %s\n", Version, Commit, CommitDate, TreeState)
		os.Exit(0)
	}
}

func main() {
	printVersion()

	// Load configuration first to get log level
	cfg, err := config.LoadConfig()
	if err != nil {
		// Use default logger for this error since we don't have config yet
		slog.Error("Failed to load configuration", "error", err)
		os.Exit(1)
	}

	// Setup structured logging with configured level
	var logLevel slog.Level
	err = logLevel.UnmarshalText([]byte(cfg.Log.Level))
	if err != nil {
		slog.Error("Invalid log level", "level", cfg.Log.Level, "error", err)
		os.Exit(1)
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))

	// Create rate limiter
	rateLimiter := ratelimit.New(ratelimit.Config{
		KandjiRequestsPerSecond:     cfg.RateLimits.KandjiRequestsPerSecond,
		CloudflareRequestsPerSecond: cfg.RateLimits.CloudflareRequestsPerSecond,
		BurstCapacity:               cfg.RateLimits.BurstCapacity,
	})

	// Create clients for Kandji and Cloudflare
	kandjiClient, err := kandji.NewClient(cfg.Kandji, rateLimiter)
	if err != nil {
		log.Error("Failed to create Kandji client", "error", err)
		os.Exit(1)
	}

	cloudflareClient, err := cloudflare.NewClient(cfg.Cloudflare, rateLimiter, log)
	if err != nil {
		log.Error("Failed to create Cloudflare client", "error", err)
		os.Exit(1)
	}

	// Validate that the Cloudflare list exists
	if err := cloudflareClient.ValidateListExists(context.Background()); err != nil {
		log.Error("Failed to validate Cloudflare list! This likely means you don't have access to the list or the list ID is wrong.", "error", err)
		os.Exit(1)
	}

	// Create and start the syncer
	syncService := syncer.New(kandjiClient, cloudflareClient, cfg, log)

	// Set up context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Listen for interrupt signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Info("Shutdown signal received, stopping service...")
		cancel()
	}()

	// Start the main sync loop
	syncService.Run(ctx, cfg.SyncInterval)

	log.Info("Service has shut down gracefully.")
}
