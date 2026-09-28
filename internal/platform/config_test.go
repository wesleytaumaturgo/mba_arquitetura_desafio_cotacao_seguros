package platform

import (
	"reflect"
	"testing"
	"time"
)

func env(pairs map[string]string) func(string) string {
	return func(key string) string { return pairs[key] }
}

func TestDefaultConfigPointsAtTheComposeEnvironment(t *testing.T) {
	cfg, err := LoadConfig(env(nil))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.Port != "8080" {
		t.Errorf("port %q, want 8080", cfg.Port)
	}
	want := []Partner{
		{Name: "partner-slow", BaseURL: "http://localhost:9001"},
		{Name: "partner-flaky", BaseURL: "http://localhost:9002"},
		{Name: "partner-degrading", BaseURL: "http://localhost:9003"},
	}
	if !reflect.DeepEqual(cfg.Partners, want) {
		t.Errorf("partners %+v, want %+v", cfg.Partners, want)
	}
	if !reflect.DeepEqual(cfg.Tenants, []string{"corretora-a", "corretora-b"}) {
		t.Errorf("brokers %v, want corretora-a and corretora-b", cfg.Tenants)
	}
}

func TestConfigReadsPartnersAndTenantsFromTheEnvironment(t *testing.T) {
	cfg, err := LoadConfig(env(map[string]string{
		"PORT":              "9090",
		"PARTNER_ENDPOINTS": " a=http://a:8080/ , b=http://b:8080 ",
		"TENANTS":           " corretora-x , corretora-y ",
	}))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	want := []Partner{{Name: "a", BaseURL: "http://a:8080"}, {Name: "b", BaseURL: "http://b:8080"}}
	if !reflect.DeepEqual(cfg.Partners, want) {
		t.Errorf("partners %+v, want %+v", cfg.Partners, want)
	}
	if !reflect.DeepEqual(cfg.Tenants, []string{"corretora-x", "corretora-y"}) {
		t.Errorf("unexpected brokers %v", cfg.Tenants)
	}
	if cfg.Port != "9090" {
		t.Errorf("port %q, want 9090", cfg.Port)
	}
}

func TestTelemetryIsOnByDefault(t *testing.T) {
	cfg, err := LoadConfig(env(nil))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	want := Telemetry{ServiceName: "quotation-api", Endpoint: "http://localhost:4317", Enabled: true}
	if cfg.Telemetry != want {
		t.Errorf("telemetry %+v, want %+v", cfg.Telemetry, want)
	}
}

func TestTelemetryReadsTheStandardOTelVariables(t *testing.T) {
	cfg, err := LoadConfig(env(map[string]string{
		"OTEL_SERVICE_NAME":           "quotation-api-poc",
		"OTEL_EXPORTER_OTLP_ENDPOINT": " http://otel-collector:4317 ",
	}))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	want := Telemetry{ServiceName: "quotation-api-poc", Endpoint: "http://otel-collector:4317", Enabled: true}
	if cfg.Telemetry != want {
		t.Errorf("telemetry %+v, want %+v", cfg.Telemetry, want)
	}
}

func TestTelemetryCanBeTurnedOff(t *testing.T) {
	cfg, err := LoadConfig(env(map[string]string{"OTEL_SDK_DISABLED": "TRUE"}))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.Telemetry.Enabled {
		t.Error("OTEL_SDK_DISABLED=TRUE did not turn the SDK off")
	}
}

func TestTelemetryOffSkipsEndpointValidation(t *testing.T) {
	if _, err := LoadConfig(env(map[string]string{
		"OTEL_SDK_DISABLED":           "true",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "not-a-url",
	})); err != nil {
		t.Fatalf("LoadConfig with the SDK off: %v", err)
	}
}

func TestConfigReadsTheResilienceVariables(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		cfg, err := LoadConfig(env(nil))
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}

		if cfg.CacheQuoteTTL != 900*time.Second {
			t.Errorf("CacheQuoteTTL = %v, want 900s", cfg.CacheQuoteTTL)
		}
		if cfg.PartnerTimeout != 2000*time.Millisecond {
			t.Errorf("PartnerTimeout = %v, want 2000ms", cfg.PartnerTimeout)
		}
		wantBreaker := Breaker{ConsecutiveFailures: 5, OpenTimeout: 5 * time.Second, HalfOpenMaxRequests: 2}
		if cfg.Breaker != wantBreaker {
			t.Errorf("Breaker = %+v, want %+v", cfg.Breaker, wantBreaker)
		}
		if cfg.RedisAddr != "redis:6379" {
			t.Errorf("RedisAddr = %q, want redis:6379", cfg.RedisAddr)
		}
	})

	t.Run("customized", func(t *testing.T) {
		cfg, err := LoadConfig(env(map[string]string{
			"CACHE_QUOTE_TTL_SECONDS":        "60",
			"PARTNER_TIMEOUT_MS":             "500",
			"BREAKER_CONSECUTIVE_FAILURES":   "3",
			"BREAKER_OPEN_SECONDS":           "10",
			"BREAKER_HALF_OPEN_MAX_REQUESTS": "4",
			"REDIS_ADDR":                     "localhost:6380",
		}))
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}

		if cfg.CacheQuoteTTL != 60*time.Second {
			t.Errorf("CacheQuoteTTL = %v, want 60s", cfg.CacheQuoteTTL)
		}
		if cfg.PartnerTimeout != 500*time.Millisecond {
			t.Errorf("PartnerTimeout = %v, want 500ms", cfg.PartnerTimeout)
		}
		wantBreaker := Breaker{ConsecutiveFailures: 3, OpenTimeout: 10 * time.Second, HalfOpenMaxRequests: 4}
		if cfg.Breaker != wantBreaker {
			t.Errorf("Breaker = %+v, want %+v", cfg.Breaker, wantBreaker)
		}
		if cfg.RedisAddr != "localhost:6380" {
			t.Errorf("RedisAddr = %q, want localhost:6380", cfg.RedisAddr)
		}
	})

	t.Run("empty REDIS_ADDR falls back to the default", func(t *testing.T) {
		cfg, err := LoadConfig(env(map[string]string{"REDIS_ADDR": ""}))
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.RedisAddr != "redis:6379" {
			t.Errorf("RedisAddr = %q, want redis:6379 (default)", cfg.RedisAddr)
		}
	})
}

func TestInvalidConfigFails(t *testing.T) {
	cases := map[string]map[string]string{
		"partner without url":                            {"PARTNER_ENDPOINTS": "partner-slow"},
		"relative url":                                   {"PARTNER_ENDPOINTS": "partner-slow=/quotes"},
		"url without host":                               {"PARTNER_ENDPOINTS": "partner-slow=http://"},
		"partner without name":                           {"PARTNER_ENDPOINTS": "=http://a:8080"},
		"repeated partner":                               {"PARTNER_ENDPOINTS": "a=http://a:8080,a=http://b:8080"},
		"empty partner list":                             {"PARTNER_ENDPOINTS": " , "},
		"empty tenant list":                              {"TENANTS": " , "},
		"collector without scheme":                       {"OTEL_EXPORTER_OTLP_ENDPOINT": "otel-collector:4317"},
		"collector without host":                         {"OTEL_EXPORTER_OTLP_ENDPOINT": "http://"},
		"OTEL_SDK_DISABLED is not a boolean":             {"OTEL_SDK_DISABLED": "maybe"},
		"CACHE_QUOTE_TTL_SECONDS is not a number":        {"CACHE_QUOTE_TTL_SECONDS": "one hour"},
		"PARTNER_TIMEOUT_MS is not a number":             {"PARTNER_TIMEOUT_MS": "fast"},
		"BREAKER_CONSECUTIVE_FAILURES is not a number":   {"BREAKER_CONSECUTIVE_FAILURES": "five"},
		"BREAKER_OPEN_SECONDS is not a number":           {"BREAKER_OPEN_SECONDS": "-5"},
		"BREAKER_HALF_OPEN_MAX_REQUESTS is not a number": {"BREAKER_HALF_OPEN_MAX_REQUESTS": "two"},
		"REDIS_ADDR without a port":                      {"REDIS_ADDR": "redis-only-host"},
		"REDIS_ADDR with a scheme instead of host:port":  {"REDIS_ADDR": "redis://redis:6379"},
	}

	for name, vars := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadConfig(env(vars)); err == nil {
				t.Fatalf("invalid configuration (%v) was accepted", vars)
			}
		})
	}
}
