package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"kandji-cloudflare-device-sync/cloudflare"
	"kandji-cloudflare-device-sync/config"
	"kandji-cloudflare-device-sync/internal/ratelimit"
	"kandji-cloudflare-device-sync/kandji"
	"kandji-cloudflare-device-sync/syncer"
)

var (
	Version    = "dev"
	Commit     = "n/a"
	CommitDate = "n/a"
	TreeState  = "n/a"
)

func main() {
	showVersion := false
	for _, arg := range os.Args[1:] {
		if arg == "-version" || arg == "--version" {
			showVersion = true
			break
		}
	}
	if showVersion {
		fmt.Printf("%s, %s, %s, %s\n", Version, Commit, CommitDate, TreeState)
		os.Exit(0)
	}

	cfg, err := config.ParseConfig()
	if err != nil {
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

	// Debug: List devices already in the target Cloudflare list
	if logLevel == slog.LevelDebug {
		targetSerials, err := cloudflareClient.GetListItems(context.Background())
		if err != nil {
			log.Error("Failed to fetch devices from target Cloudflare list", "error", err)
		} else {
			log.Debug("Devices already in target Cloudflare list", "count", len(targetSerials), "serials", targetSerials)
		}
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
