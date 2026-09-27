package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/cache"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/partner"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/platform"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/quotation"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/resilience"
)

const meterName = "quotation-api"

func main() {
	cfg, err := platform.LoadConfig(platform.Environment())
	if err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}

	ctx, stopListening := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopListening()

	stopTelemetry, err := platform.StartTelemetry(context.Background(), cfg.Telemetry)
	if err != nil {
		log.Fatalf("telemetry: %v", err)
	}

	redisClient := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})

	breakerGauge, err := platform.NewPartnerBreakerGauge(otel.Meter(meterName))
	if err != nil {
		log.Fatalf("partner breaker gauge: %v", err)
	}

	quoteCache := cache.NewQuoteCache(redisClient, cfg.CacheQuoteTTL, cache.WithMeter(otel.Meter(meterName)))

	breakers := make(map[string]*resilience.Breaker, len(cfg.Partners))
	for _, p := range cfg.Partners {
		breakers[p.Name] = resilience.NewBreaker(p.Name, resilience.Config{
			ConsecutiveFailures: cfg.Breaker.ConsecutiveFailures,
			OpenTimeout:         cfg.Breaker.OpenTimeout,
			HalfOpenMaxRequests: cfg.Breaker.HalfOpenMaxRequests,
			OnStateChange:       breakerGauge.OnStateChange,
		})
	}

	api := quotation.NewAPI(
		quotation.NewService(
			cfg.Partners, partner.NewClient(cfg.PartnerTimeout), quoteCache, breakers,
			quotation.WithTracer(otel.Tracer(meterName)),
		),
		cfg.Tenants,
	)

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           platform.InstrumentHandler(api.Routes(), cfg.Telemetry.ServiceName),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		log.Print("quotation-api: signal received, shutting down")
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			log.Printf("quotation-api: forced shutdown: %v", err)
		}
		if err := stopTelemetry(shutdown); err != nil {
			log.Printf("quotation-api: telemetry shutdown: %v", err)
		}
	}()

	log.Printf("quotation-api listening on %s | partners=%d brokers=%v",
		server.Addr, len(cfg.Partners), cfg.Tenants)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server stopped: %v", err)
	}
}
