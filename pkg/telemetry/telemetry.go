package telemetry

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

const InstrumentationName = "github.com/QuantumNous/new-api"

// Init configures OTLP trace export when an OTLP endpoint is present. Keeping
// tracing disabled without an endpoint preserves the existing zero-config
// deployment behavior.
func Init(ctx context.Context) (func(context.Context) error, error) {
	if disabled, _ := strconv.ParseBool(os.Getenv("OTEL_SDK_DISABLED")); disabled {
		return func(context.Context) error { return nil }, nil
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("OTEL_TRACES_EXPORTER")), "none") {
		return func(context.Context) error { return nil }, nil
	}
	if os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("initialize OTLP trace exporter: %w", err)
	}

	res := resource.Default()
	if os.Getenv("OTEL_SERVICE_NAME") == "" {
		res, err = resource.Merge(res, resource.NewSchemaless(
			semconv.ServiceNameKey.String("new-api"),
			attribute.String("service.namespace", "new-api"),
		))
		if err != nil {
			return nil, fmt.Errorf("initialize OpenTelemetry resource: %w", err)
		}
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(samplerFromEnv()),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return func(shutdownCtx context.Context) error {
		return provider.Shutdown(shutdownCtx)
	}, nil
}

func samplerFromEnv() sdktrace.Sampler {
	name := strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER")))
	argument := strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER_ARG"))
	ratio, err := strconv.ParseFloat(argument, 64)
	if err != nil || ratio < 0 || ratio > 1 {
		ratio = 1
	}

	switch name {
	case "always_off":
		return sdktrace.NeverSample()
	case "traceidratio":
		return sdktrace.TraceIDRatioBased(ratio)
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample())
	case "parentbased_traceidratio":
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
	case "always_on":
		return sdktrace.AlwaysSample()
	case "", "parentbased_always_on":
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	default:
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	}
}

// ShutdownTimeout bounds the final exporter flush independently from the HTTP
// server drain timeout.
func ShutdownTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}
