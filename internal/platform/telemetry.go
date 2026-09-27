package platform

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/sony/gobreaker/v2"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

const metricInterval = 10 * time.Second

type Shutdown func(context.Context) error

func StartTelemetry(ctx context.Context, cfg Telemetry) (Shutdown, error) {
	if !cfg.Enabled {
		log.Print("quotation-api: OpenTelemetry off (OTEL_SDK_DISABLED)")
		return func(context.Context) error { return nil }, nil
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(cfg.ServiceName)),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry resource: %w", err)
	}

	traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(cfg.Endpoint))
	if err != nil {
		return nil, fmt.Errorf("otlp trace exporter: %w", err)
	}
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(traceExporter),

		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	metricExporter, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithEndpointURL(cfg.Endpoint))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("otlp metric exporter: %w", err), tracerProvider.Shutdown(ctx))
	}
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(metricInterval))),
	)

	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if err := runtime.Start(runtime.WithMeterProvider(meterProvider)); err != nil {
		return nil, errors.Join(
			fmt.Errorf("runtime metrics: %w", err),
			tracerProvider.Shutdown(ctx),
			meterProvider.Shutdown(ctx),
		)
	}

	log.Printf("quotation-api: OpenTelemetry on | service=%q collector=%s", cfg.ServiceName, cfg.Endpoint)

	return func(ctx context.Context) error {
		return errors.Join(tracerProvider.Shutdown(ctx), meterProvider.Shutdown(ctx))
	}, nil
}

func InstrumentHandler(h http.Handler, serviceName string) http.Handler {
	return otelhttp.NewHandler(h, serviceName,
		otelhttp.WithFilter(func(r *http.Request) bool { return r.URL.Path != "/healthz" }),

		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	)
}

func InstrumentTransport(base http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(base)
}

// PartnerBreakerGauge reports partner_breaker_state{partner} (T09b, FDD seção 7): 0 closed, 1
// half-open, 2 open. It never calls the collector itself; it only keeps the last known state per
// partner (fed synchronously and cheaply through OnStateChange, meant to be wired into
// resilience.Config.OnStateChange) and hands that map over when the SDK asks for it via
// RegisterCallback, at actual collection time. partner is the gauge's only attribute: low
// cardinality (one series per configured partner), never tenant_id or a quote_id.
type PartnerBreakerGauge struct {
	mu     sync.Mutex
	states map[string]int64
}

// NewPartnerBreakerGauge registers the partner_breaker_state gauge on m and returns the
// *PartnerBreakerGauge that backs it.
func NewPartnerBreakerGauge(m metric.Meter) (*PartnerBreakerGauge, error) {
	g := &PartnerBreakerGauge{states: make(map[string]int64)}

	gauge, err := m.Int64ObservableGauge(
		"partner_breaker_state",
		metric.WithDescription("Circuit breaker state per partner: 0 closed, 1 half-open, 2 open."),
	)
	if err != nil {
		return nil, fmt.Errorf("partner_breaker_state gauge: %w", err)
	}

	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		g.mu.Lock()
		defer g.mu.Unlock()
		for name, state := range g.states {
			o.ObserveInt64(gauge, state, metric.WithAttributes(attribute.String("partner", name)))
		}
		return nil
	}, gauge)
	if err != nil {
		return nil, fmt.Errorf("partner_breaker_state callback: %w", err)
	}

	return g, nil
}

// OnStateChange has the exact signature of resilience.Config.OnStateChange, so it can be assigned to
// that field directly (e.g. resilience.Config{OnStateChange: gauge.OnStateChange, ...}). It only
// records to; from is unused (the gauge only ever reports the current state).
func (g *PartnerBreakerGauge) OnStateChange(name string, _, to gobreaker.State) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.states[name] = breakerStateValue(to)
}

// breakerStateValue maps gobreaker.State to the gauge's contract: 0 closed, 1 half-open, 2 open.
func breakerStateValue(s gobreaker.State) int64 {
	switch s {
	case gobreaker.StateHalfOpen:
		return 1
	case gobreaker.StateOpen:
		return 2
	default:
		return 0
	}
}
