package middleware

import (
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	apptelemetry "github.com/QuantumNous/new-api/pkg/telemetry"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// OpenTelemetry extracts an incoming W3C trace context and creates the HTTP
// server span used as the parent of relay and upstream-provider spans.
func OpenTelemetry() gin.HandlerFunc {
	tracer := otel.Tracer(apptelemetry.InstrumentationName)
	return func(c *gin.Context) {
		request := c.Request
		ctx := otel.GetTextMapPropagator().Extract(request.Context(), propagation.HeaderCarrier(request.Header))
		ctx, span := tracer.Start(ctx, request.Method+" "+request.URL.Path,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.request.method", request.Method),
				attribute.String("url.path", request.URL.Path),
				attribute.String("server.address", request.Host),
			),
		)
		c.Request = request.WithContext(ctx)

		defer func() {
			recovered := recover()
			route := c.FullPath()
			if route == "" {
				route = request.URL.Path
			}
			statusCode := c.Writer.Status()
			span.SetName(request.Method + " " + route)
			span.SetAttributes(
				attribute.String("http.route", route),
				attribute.Int("http.response.status_code", statusCode),
				attribute.String("new_api.request.id", c.GetString(common.RequestIdKey)),
			)
			if recovered != nil {
				span.SetStatus(codes.Error, "panic")
			} else if statusCode >= http.StatusInternalServerError {
				span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", statusCode))
			}
			span.End()
			if recovered != nil {
				panic(recovered)
			}
		}()

		c.Next()
	}
}
