package main

import (
	"context"
	"net/http"
	"strings"

	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
	"github.com/uber/jaeger-client-go"
)

const (
	tagHTTPStatusCode            = "http.status_code"
	tagHTTPStatus                = "http.status"
	tagHTTPRequestContentLength  = "http.request.content_length"
	tagHTTPResponseContentLength = "http.response.content_length"
)

func tagHTTPRequestHeader(h string) string  { return "http.request.header." + h }
func tagHTTPResponseHeader(h string) string { return "http.response.header." + h }

// logTraceID - adds a traceID field to the request log
// Intended to be used with nethttp.MWSpanObserver
func logTraceID(span opentracing.Span, r *http.Request) {
	tid := getTraceID(span)
	if tid != "" {
		log := hlog.FromRequest(r)
		log.UpdateContext(func(c zerolog.Context) zerolog.Context {
			return c.Str("traceID", tid)
		})
	}
}

// getTraceID - attempt to pull the trace ID out of the span. This only
// works when using the Jaeger client
func getTraceID(span opentracing.Span) string {
	if sc, ok := span.Context().(jaeger.SpanContext); ok {
		return sc.TraceID().String()
	}
	return ""
}

func tagsFromRequest(span opentracing.Span, r *http.Request) {
	for k, h := range r.Header {
		if sensitiveHeader(k) {
			continue
		}
		span.SetTag(tagHTTPRequestHeader(k), strings.Join(h, "\n"))
	}
	span.SetTag(tagHTTPRequestContentLength, r.ContentLength)

	ext.HTTPUrl.Set(span, r.URL.String())
}

func tagsFromResponse(span opentracing.Span, r *http.Response) {
	for k, h := range r.Header {
		span.SetTag(tagHTTPResponseHeader(k), strings.Join(h, "\n"))
	}
	span.SetTag(tagHTTPResponseContentLength, r.ContentLength)
	span.SetTag(tagHTTPStatus, r.Status)
	span.SetTag(tagHTTPStatusCode, r.StatusCode)

	ext.Error.Set(span, r.StatusCode > 399)
}

func sensitiveHeader(h string) bool {
	switch strings.ToLower(h) {
	case "authorization":
		return true
	default:
		return false
	}
}

func createSpan(ctx context.Context, operationName string) (opentracing.Span, context.Context) {
	tracer := opentracing.GlobalTracer()
	spanOpts := []opentracing.StartSpanOption{}
	parent := opentracing.SpanFromContext(ctx)
	if parent != nil {
		spanOpts = append(spanOpts, opentracing.ChildOf(parent.Context()))
	}
	span := tracer.StartSpan(operationName, spanOpts...)
	return span, opentracing.ContextWithSpan(ctx, span)
}
