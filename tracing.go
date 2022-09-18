package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"runtime/debug"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.12.0"
	"go.opentelemetry.io/otel/trace"
)

func tagHTTPRequestHeader(h string) string  { return "http.request.header." + h }
func tagHTTPResponseHeader(h string) string { return "http.response.header." + h }

func tagsFromRequest(span trace.Span, r *http.Request) {
	if r == nil {
		return
	}

	hdrLabels := make([]attribute.KeyValue, len(r.Header))
	i := 0
	for k, h := range r.Header {
		hdrLabels[i] = attribute.String(tagHTTPRequestHeader(k), strings.Join(h, "\n"))
		i++
	}
	span.SetAttributes(hdrLabels...)
	span.SetAttributes(semconv.HTTPClientAttributesFromHTTPRequest(r)...)
}

func tagsFromResponse(span trace.Span, r *http.Response) {
	if r == nil {
		return
	}

	hdrLabels := make([]attribute.KeyValue, len(r.Header))
	i := 0
	for k, h := range r.Header {
		hdrLabels[i] = attribute.String(tagHTTPResponseHeader(k), strings.Join(h, "\n"))
		i++
	}
	span.SetAttributes(hdrLabels...)
	span.SetAttributes(semconv.HTTPResponseContentLengthKey.Int64(r.ContentLength))
	span.SetAttributes(semconv.HTTPAttributesFromHTTPStatusCode(r.StatusCode)...)
}

func initTracer(ctx context.Context, exporter sdktrace.SpanExporter) error {
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("failed to lookup hostname: %w", err)
	}
	version := "unknown"
	module := "unknown"
	if bi, ok := debug.ReadBuildInfo(); ok {
		version = bi.Main.Version
		module = bi.Main.Path
		// sum = bi.Main.Sum
	}
	res, err := resource.New(ctx, resource.WithAttributes(
		semconv.ServiceNameKey.String("wnp-bridge"),
		semconv.ServiceInstanceIDKey.String(hostname),
		attribute.String("module.path", module),
		semconv.ServiceVersionKey.String(version),
	))
	if err != nil {
		return fmt.Errorf("failed to create resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	return nil
}
