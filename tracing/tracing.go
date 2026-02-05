// Package tracing provides stub implementations for tracing functionality
package tracing

import (
	"context"
	"net/http"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/trace"
)

// Re-export trace.Span for compatibility
type Span = trace.Span

const (
	TracerContextName                 = "cf-trace-id"
	CanonicalCloudflaredTracingHeader = "Cf-Cloudflared-Tracing"
	IntCloudflaredTracingHeader       = "cf-cloudflared-tracing"
	IdentityLength                    = 16
)

// TracedHTTPRequest wraps an HTTP request with tracing context
type TracedHTTPRequest struct {
	*http.Request
	ConnIndex uint8
	Log       *zerolog.Logger
}

// NewTracedHTTPRequest creates a new traced HTTP request
func NewTracedHTTPRequest(req *http.Request, connIndex uint8, log *zerolog.Logger) *TracedHTTPRequest {
	return &TracedHTTPRequest{
		Request:   req,
		ConnIndex: connIndex,
		Log:       log,
	}
}

// Tracer returns a noop tracer
func (tr *TracedHTTPRequest) Tracer() trace.Tracer {
	return trace.NewNoopTracerProvider().Tracer("")
}

// ToTracedContext converts to TracedContext
func (tr *TracedHTTPRequest) ToTracedContext() *TracedContext {
	return &TracedContext{
		Context: tr.Request.Context(),
		log:     tr.Log,
	}
}

// AddSpans adds spans to the request (noop)
func (tr *TracedHTTPRequest) AddSpans(headers http.Header) {
}

// TracedContext wraps a context with tracing information
type TracedContext struct {
	context.Context
	identity string
	log      *zerolog.Logger
}

// NewTracedContext creates a new traced context
func NewTracedContext(ctx context.Context, identity string, log *zerolog.Logger) *TracedContext {
	return &TracedContext{
		Context:  ctx,
		identity: identity,
		log:      log,
	}
}

// Tracer returns a noop tracer
func (tc *TracedContext) Tracer() trace.Tracer {
	return trace.NewNoopTracerProvider().Tracer("")
}

// Logger returns the logger
func (tc *TracedContext) Logger() *zerolog.Logger {
	return tc.log
}

// GetProtoSpans returns empty spans for protocol tracing
func (tc *TracedContext) GetProtoSpans() []byte {
	return nil
}

// GetSpans returns empty spans
func (tc *TracedContext) GetSpans() string {
	return ""
}

// Identity represents a tracing identity
type Identity struct {
	identity string
}

// String returns the string representation
func (i Identity) String() string {
	return i.identity
}

// UnmarshalBinary deserializes the identity
func (i *Identity) UnmarshalBinary(data []byte) error {
	i.identity = string(data)
	return nil
}

// NewNoopSpan returns a noop span
func NewNoopSpan() trace.Span {
	return trace.SpanFromContext(context.Background())
}

// End ends the span
func End(span trace.Span) {
	if span != nil {
		span.End()
	}
}

// EndWithErrorStatus ends span with error
func EndWithErrorStatus(span trace.Span, err error) {
	if span != nil {
		span.End()
	}
}

// EndWithStatusCode ends span with status code
func EndWithStatusCode(span trace.Span, statusCode int) {
	if span != nil {
		span.End()
	}
}
