package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTracedRoundTripperPropagatesTraceContext(t *testing.T) {
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

	traceparent := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		traceparent <- request.Header.Get("traceparent")
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	ctx, parent := otel.Tracer("test").Start(context.Background(), "relay-attempt")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.URL, nil)
	require.NoError(t, err)
	client := &http.Client{Transport: newTracedRoundTripper(http.DefaultTransport)}
	response, err := client.Do(request)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	parent.End()

	assert.NotEmpty(t, <-traceparent)
	spans := recorder.Ended()
	require.Len(t, spans, 2)
	assert.Equal(t, "llm.upstream POST", spans[0].Name())
	assert.Equal(t, spans[1].SpanContext().SpanID(), spans[0].Parent().SpanID())
}
