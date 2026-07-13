package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/pkg/telemetry"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestOpenTelemetryContinuesIncomingTrace(t *testing.T) {
	originalProvider := otel.GetTracerProvider()
	originalPropagator := otel.GetTextMapPropagator()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(originalProvider)
		otel.SetTextMapPropagator(originalPropagator)
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(OpenTelemetry())
	router.GET("/v1/test", func(c *gin.Context) {
		_, child := otel.Tracer(telemetry.InstrumentationName).Start(c.Request.Context(), "relay-child")
		child.End()
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
	request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusNoContent, response.Code)
	spans := recorder.Ended()
	require.Len(t, spans, 2)
	assert.Equal(t, "relay-child", spans[0].Name())
	assert.Equal(t, "GET /v1/test", spans[1].Name())
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", spans[1].SpanContext().TraceID().String())
	assert.Equal(t, "00f067aa0ba902b7", spans[1].Parent().SpanID().String())
	assert.Equal(t, spans[1].SpanContext().SpanID(), spans[0].Parent().SpanID())
}
