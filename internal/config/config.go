// Package config loads and validates environment-based configuration.
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hundrik3/L7-SRE/internal/limiter"
)

type Config struct {
	ListenAddr        string
	AdminAddr         string
	Backends          []string
	Strategy          string
	MaxAttempts       int
	UpstreamTimeout   time.Duration
	DialTimeout       time.Duration
	HealthInterval    time.Duration
	HealthTimeout     time.Duration
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	TrustedProxies    []string
	Limiter           limiter.Config
}

func Load() (Config, error) { return load(os.LookupEnv) }

func load(lookup func(string) (string, bool)) (Config, error) {
	var errs []string
	value := func(key, fallback string) string {
		if s, ok := lookup(key); ok {
			return s
		}
		return fallback
	}
	integer := func(key string, fallback int) int {
		n, err := strconv.Atoi(value(key, strconv.Itoa(fallback)))
		if err != nil || n <= 0 {
			errs = append(errs, key+" must be a positive integer")
		}
		return n
	}
	duration := func(key, fallback string) time.Duration {
		d, err := time.ParseDuration(value(key, fallback))
		if err != nil || d <= 0 {
			errs = append(errs, key+" must be a positive duration")
		}
		return d
	}
	rate, err := strconv.ParseFloat(value("RATE_LIMIT_RPS", "100"), 64)
	if err != nil {
		errs = append(errs, "RATE_LIMIT_RPS must be a number")
	}
	c := Config{
		ListenAddr: value("LISTEN_ADDR", ":8080"), AdminAddr: value("ADMIN_ADDR", ":9090"),
		Backends: split(value("BACKENDS", "")), Strategy: value("BALANCER_STRATEGY", "round_robin"),
		MaxAttempts:     integer("MAX_ATTEMPTS", 3),
		UpstreamTimeout: duration("UPSTREAM_TIMEOUT", "5s"), DialTimeout: duration("DIAL_TIMEOUT", "1s"),
		HealthInterval: duration("HEALTH_INTERVAL", "5s"), HealthTimeout: duration("HEALTH_TIMEOUT", "1s"),
		ReadHeaderTimeout: duration("READ_HEADER_TIMEOUT", "5s"), ReadTimeout: duration("READ_TIMEOUT", "15s"),
		WriteTimeout: duration("WRITE_TIMEOUT", "20s"), IdleTimeout: duration("IDLE_TIMEOUT", "60s"),
		ShutdownTimeout: duration("SHUTDOWN_TIMEOUT", "25s"), TrustedProxies: split(value("TRUSTED_PROXY_CIDRS", "")),
		Limiter: limiter.Config{Rate: rate, Burst: integer("RATE_LIMIT_BURST", 200), MaxClients: integer("RATE_LIMIT_MAX_CLIENTS", 100000), IdleTTL: duration("RATE_LIMIT_IDLE_TTL", "5m"), CleanupInterval: duration("RATE_LIMIT_CLEANUP_INTERVAL", "1m")},
	}
	for _, addr := range []string{c.ListenAddr, c.AdminAddr} {
		_, port, err := net.SplitHostPort(addr)
		p, perr := strconv.Atoi(port)
		if err != nil || perr != nil || p < 1 || p > 65535 {
			errs = append(errs, "invalid listen address: "+addr)
		}
	}
	if c.ListenAddr == c.AdminAddr {
		errs = append(errs, "data and admin addresses must differ")
	}
	if len(c.Backends) == 0 {
		errs = append(errs, "BACKENDS must list at least one HTTP(S) backend")
	}
	if c.Strategy != "round_robin" && c.Strategy != "least_connections" {
		errs = append(errs, "BALANCER_STRATEGY must be round_robin or least_connections")
	}
	if c.MaxAttempts > 10 {
		errs = append(errs, "MAX_ATTEMPTS must not exceed 10")
	}
	if _, err := limiter.New(c.Limiter); err != nil {
		errs = append(errs, err.Error())
	}
	if _, err := limiter.NewIPResolver(c.TrustedProxies); err != nil {
		errs = append(errs, "TRUSTED_PROXY_CIDRS: "+err.Error())
	}
	if c.WriteTimeout <= c.UpstreamTimeout {
		errs = append(errs, "WRITE_TIMEOUT must exceed UPSTREAM_TIMEOUT")
	}
	if len(errs) > 0 {
		return Config{}, fmt.Errorf("configuration: %s", strings.Join(errs, "; "))
	}
	return c, nil
}

func split(s string) []string {
	var result []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
