package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

// Config holds all configuration for the application.
type Config struct {
	SyncInterval time.Duration    `yaml:"sync_interval"`
	OnMissing    string           `yaml:"on_missing"`
	Kandji       KandjiConfig     `yaml:"kandji"`
	Cloudflare   CloudflareConfig `yaml:"cloudflare"`
	RateLimits   RateLimitConfig  `yaml:"rate_limits"`
	Batch        BatchConfig      `yaml:"batch"`
	Log          LoggingConfig    `yaml:"log"`
}

type BlueprintFilter struct {
	BlueprintIDs   []string `yaml:"blueprint_ids"`
	BlueprintNames []string `yaml:"blueprint_names"`
}

type LoggingConfig struct {
	Level string `yaml:"level"`
}

type KandjiConfig struct {
	ApiURL                   string          `yaml:"api_url"`
	ApiToken                 string          `yaml:"api_token"`
	SyncDevicesWithoutOwners bool            `yaml:"sync_devices_without_owners"`
	SyncMobileDevices        bool            `yaml:"sync_mobile_devices"`
	IncludeTags              []string        `yaml:"include_tags"`
	ExcludeTags              []string        `yaml:"exclude_tags"`
	BlueprintsInclude        BlueprintFilter `yaml:"blueprints_include"`
	BlueprintsExclude        BlueprintFilter `yaml:"blueprints_exclude"`
}

type CloudflareConfig struct {
	ApiToken      string   `yaml:"api_token"`
	AccountID     string   `yaml:"account_id"`
	ListID        string   `yaml:"target_list_id"`
	SourceListIDs []string `yaml:"source_list_ids"`
}

// RateLimitConfig holds rate limiting settings.
type RateLimitConfig struct {
	KandjiRequestsPerSecond     float64 `yaml:"kandji_requests_per_second"`
	CloudflareRequestsPerSecond float64 `yaml:"cloudflare_requests_per_second"`
	BurstCapacity               int     `yaml:"burst_capacity"`
}

// BatchConfig holds batch processing settings.
type BatchConfig struct {
	Size                 int `yaml:"size"`
	MaxConcurrentBatches int `yaml:"max_concurrent_batches"`
}

func LoadConfig() (*Config, error) {
	cfg := &Config{}

	// Load from config file if it exists
	if _, err := os.Stat("config.yaml"); err == nil {
		data, err := os.ReadFile("config.yaml")
		if err != nil {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}

		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config file: %w", err)
		}
	}

	// Override with environment variables if set
	if url := os.Getenv("KANDJI_API_URL"); url != "" {
		cfg.Kandji.ApiURL = url
	}
	if token := os.Getenv("KANDJI_API_TOKEN"); token != "" {
		cfg.Kandji.ApiToken = token
	}
	if token := os.Getenv("CLOUDFLARE_API_TOKEN"); token != "" {
		cfg.Cloudflare.ApiToken = token
	}
	if accountID := os.Getenv("CLOUDFLARE_ACCOUNT_ID"); accountID != "" {
		cfg.Cloudflare.AccountID = accountID
	}
	if listID := os.Getenv("CLOUDFLARE_LIST_ID"); listID != "" {
		cfg.Cloudflare.ListID = listID
	}
	if sourceListIDs := os.Getenv("CLOUDFLARE_SOURCE_LIST_IDS"); sourceListIDs != "" {
		cfg.Cloudflare.SourceListIDs = strings.Split(sourceListIDs, ",")
	}
	if onMissing := os.Getenv("ON_MISSING"); onMissing != "" {
		cfg.OnMissing = onMissing
	}
	if syncWithoutOwners := os.Getenv("SYNC_DEVICES_WITHOUT_OWNERS"); syncWithoutOwners != "" {
		cfg.Kandji.SyncDevicesWithoutOwners = strings.ToLower(syncWithoutOwners) == "true"
	}
	if SyncMobileDevices := os.Getenv("SYNC_MOBILE_DEVICES"); SyncMobileDevices != "" {
		cfg.Kandji.SyncMobileDevices = strings.ToLower(SyncMobileDevices) == "true"
	}
	if logLevel := os.Getenv("LOG_LEVEL"); logLevel != "" {
		cfg.Log.Level = logLevel
	}

	// Set default sync interval if not specified
	if cfg.SyncInterval == 0 {
		cfg.SyncInterval = 5 * time.Minute
	}

	// Set default on_missing behavior if not specified
	if cfg.OnMissing == "" {
		cfg.OnMissing = "ignore"
	}

	// Set default rate limits if not specified
	if cfg.RateLimits.KandjiRequestsPerSecond == 0 {
		cfg.RateLimits.KandjiRequestsPerSecond = 10.0
	}
	if cfg.RateLimits.CloudflareRequestsPerSecond == 0 {
		cfg.RateLimits.CloudflareRequestsPerSecond = 4.0 // Cloudflare has stricter limits
	}
	if cfg.RateLimits.BurstCapacity == 0 {
		cfg.RateLimits.BurstCapacity = 5
	}

	// Set default batch settings if not specified
	if cfg.Batch.Size == 0 {
		cfg.Batch.Size = 50
	}
	if cfg.Batch.MaxConcurrentBatches == 0 {
		cfg.Batch.MaxConcurrentBatches = 3
	}

	// Validate required configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("configuration validation failed: %w", err)
	}

	return cfg, nil
}

// Validate checks that all required configuration values are present and valid.
func (c *Config) Validate() error {
	if c.Kandji.ApiURL == "" {
		return fmt.Errorf("KANDJI_API_URL is required")
	}
	if c.Kandji.ApiToken == "" {
		return fmt.Errorf("KANDJI_API_TOKEN is required")
	}
	if c.Cloudflare.ApiToken == "" {
		return fmt.Errorf("CLOUDFLARE_API_TOKEN is required")
	}
	if c.Cloudflare.AccountID == "" {
		return fmt.Errorf("CLOUDFLARE_ACCOUNT_ID is required")
	}
	// Optional: check for duplicates in SourceListIDs or if target is in source
	for _, src := range c.Cloudflare.SourceListIDs {
		if src == c.Cloudflare.ListID {
			return fmt.Errorf("CLOUDFLARE_SOURCE_LIST_IDS cannot contain the target list ID")
		}
	}

	// Validate on_missing values
	validOnMissing := []string{"ignore", "delete", "alert"}
	isValid := false
	for _, valid := range validOnMissing {
		if c.OnMissing == valid {
			isValid = true
			break
		}
	}
	if !isValid {
		return fmt.Errorf("on_missing must be one of: %s", strings.Join(validOnMissing, ", "))
	}

	return nil
}
