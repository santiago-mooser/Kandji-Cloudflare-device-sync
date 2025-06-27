package ratelimit

import (
	"context"

	"golang.org/x/time/rate"
)

// Limiter provides rate limiting for different API endpoints
type Limiter struct {
	kandjiLimiter     *rate.Limiter
	cloudflareLimiter *rate.Limiter
}

// Config holds rate limiting configuration
type Config struct {
	KandjiRequestsPerSecond     float64
	CloudflareRequestsPerSecond float64
	BurstCapacity               int
}

// New creates a new rate limiter with the given configuration
func New(cfg Config) *Limiter {
	return &Limiter{
		kandjiLimiter:     rate.NewLimiter(rate.Limit(cfg.KandjiRequestsPerSecond), cfg.BurstCapacity),
		cloudflareLimiter: rate.NewLimiter(rate.Limit(cfg.CloudflareRequestsPerSecond), cfg.BurstCapacity),
	}
}

// WaitForKandji waits for permission to make a Kandji API request
func (l *Limiter) WaitForKandji(ctx context.Context) error {
	return l.kandjiLimiter.Wait(ctx)
}

// WaitForCloudflare waits for permission to make a Cloudflare API request
func (l *Limiter) WaitForCloudflare(ctx context.Context) error {
	return l.cloudflareLimiter.Wait(ctx)
}

// AllowKandji checks if a Kandji API request is allowed without blocking
func (l *Limiter) AllowKandji() bool {
	return l.kandjiLimiter.Allow()
}

// AllowCloudflare checks if a Cloudflare API request is allowed without blocking
func (l *Limiter) AllowCloudflare() bool {
	return l.cloudflareLimiter.Allow()
}
