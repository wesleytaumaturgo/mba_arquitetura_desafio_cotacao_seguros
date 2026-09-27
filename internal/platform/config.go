package platform

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port           string
	Partners       []Partner
	Tenants        []string
	Telemetry      Telemetry
	CacheQuoteTTL  time.Duration
	PartnerTimeout time.Duration
	Breaker        Breaker
	RedisAddr      string
}

type Partner struct {
	Name    string
	BaseURL string
}

type Telemetry struct {
	ServiceName string
	Endpoint    string
	Enabled     bool
}

type Breaker struct {
	ConsecutiveFailures uint32
	OpenTimeout         time.Duration
	HalfOpenMaxRequests uint32
}

const defaultEndpoints = "partner-slow=http://localhost:9001," +
	"partner-flaky=http://localhost:9002," +
	"partner-degrading=http://localhost:9003"

const defaultTenants = "corretora-a,corretora-b"

const defaultCollector = "http://localhost:4317"

const defaultServiceName = "quotation-api"

const (
	defaultCacheQuoteTTLSeconds       = 900
	defaultPartnerTimeoutMillis       = 2000
	defaultBreakerConsecutiveFailures = 5
	defaultBreakerOpenSeconds         = 5
	defaultBreakerHalfOpenMaxRequests = 2
	defaultRedisAddr                  = "redis:6379"
)

func LoadConfig(env func(string) string) (Config, error) {
	cfg := Config{Port: text(env, "PORT", "8080")}

	var err error
	if cfg.Partners, err = parsePartners(text(env, "PARTNER_ENDPOINTS", defaultEndpoints)); err != nil {
		return Config{}, err
	}
	if cfg.Tenants, err = parseTenants(text(env, "TENANTS", defaultTenants)); err != nil {
		return Config{}, err
	}
	if cfg.Telemetry, err = parseTelemetry(env); err != nil {
		return Config{}, err
	}
	if err = parseResilience(env, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func parseResilience(env func(string) string, cfg *Config) error {
	cacheSeconds, err := parseUint(env, "CACHE_QUOTE_TTL_SECONDS", defaultCacheQuoteTTLSeconds)
	if err != nil {
		return err
	}
	cfg.CacheQuoteTTL = time.Duration(cacheSeconds) * time.Second

	partnerMillis, err := parseUint(env, "PARTNER_TIMEOUT_MS", defaultPartnerTimeoutMillis)
	if err != nil {
		return err
	}
	cfg.PartnerTimeout = time.Duration(partnerMillis) * time.Millisecond

	consecutiveFailures, err := parseUint32(env, "BREAKER_CONSECUTIVE_FAILURES", defaultBreakerConsecutiveFailures)
	if err != nil {
		return err
	}
	cfg.Breaker.ConsecutiveFailures = consecutiveFailures

	openSeconds, err := parseUint(env, "BREAKER_OPEN_SECONDS", defaultBreakerOpenSeconds)
	if err != nil {
		return err
	}
	cfg.Breaker.OpenTimeout = time.Duration(openSeconds) * time.Second

	halfOpenMaxRequests, err := parseUint32(env, "BREAKER_HALF_OPEN_MAX_REQUESTS", defaultBreakerHalfOpenMaxRequests)
	if err != nil {
		return err
	}
	cfg.Breaker.HalfOpenMaxRequests = halfOpenMaxRequests

	cfg.RedisAddr = text(env, "REDIS_ADDR", defaultRedisAddr)
	return nil
}

func parseUint(env func(string) string, key string, fallback uint64) (uint64, error) {
	raw := text(env, key, strconv.FormatUint(fallback, 10))
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a valid non-negative integer", key, raw)
	}
	return v, nil
}

func parseUint32(env func(string) string, key string, fallback uint32) (uint32, error) {
	v, err := parseUint(env, key, uint64(fallback))
	if err != nil {
		return 0, err
	}
	return uint32(v), nil
}

func parseTelemetry(env func(string) string) (Telemetry, error) {
	telemetry := Telemetry{
		ServiceName: text(env, "OTEL_SERVICE_NAME", defaultServiceName),
		Endpoint:    strings.TrimSpace(text(env, "OTEL_EXPORTER_OTLP_ENDPOINT", defaultCollector)),
	}

	switch disabled := strings.ToLower(strings.TrimSpace(env("OTEL_SDK_DISABLED"))); disabled {
	case "", "false":
		telemetry.Enabled = true
	case "true":
		return Telemetry{Enabled: false}, nil
	default:
		return Telemetry{}, fmt.Errorf("OTEL_SDK_DISABLED: %q is neither true nor false", disabled)
	}

	if u, err := url.Parse(telemetry.Endpoint); err != nil || u.Scheme == "" || u.Host == "" {
		return Telemetry{}, fmt.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT: %q is not an absolute URL", telemetry.Endpoint)
	}
	return telemetry, nil
}

func parsePartners(raw string) ([]Partner, error) {
	var partners []Partner
	seen := map[string]bool{}

	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}

		name, endpoint, found := strings.Cut(item, "=")
		name, endpoint = strings.TrimSpace(name), strings.TrimSpace(endpoint)
		if !found || name == "" || endpoint == "" {
			return nil, fmt.Errorf("PARTNER_ENDPOINTS: %q is not in the name=url format", item)
		}

		if u, err := url.Parse(endpoint); err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("PARTNER_ENDPOINTS: %q is not an absolute URL", endpoint)
		}
		if seen[name] {
			return nil, fmt.Errorf("PARTNER_ENDPOINTS: partner %q is repeated", name)
		}

		seen[name] = true
		partners = append(partners, Partner{Name: name, BaseURL: strings.TrimRight(endpoint, "/")})
	}

	if len(partners) == 0 {
		return nil, fmt.Errorf("PARTNER_ENDPOINTS: at least one partner is required")
	}
	return partners, nil
}

func parseTenants(raw string) ([]string, error) {
	var tenants []string
	for _, t := range strings.Split(raw, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tenants = append(tenants, t)
		}
	}
	if len(tenants) == 0 {
		return nil, fmt.Errorf("TENANTS: at least one broker is required")
	}
	return tenants, nil
}

func Environment() func(string) string { return os.Getenv }

func text(env func(string) string, key, fallback string) string {
	if v := env(key); v != "" {
		return v
	}
	return fallback
}
