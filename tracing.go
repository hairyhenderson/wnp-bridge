package main

import (
	"fmt"
	"net/http"
	"os"
	"runtime/debug"
	"strings"

	"go.opentelemetry.io/otel/api/global"
	"go.opentelemetry.io/otel/api/trace"
	"go.opentelemetry.io/otel/label"
	exportTrace "go.opentelemetry.io/otel/sdk/export/trace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv"
)

func tagHTTPRequestHeader(h string) string  { return "http.request.header." + h }
func tagHTTPResponseHeader(h string) string { return "http.response.header." + h }

func tagsFromRequest(span trace.Span, r *http.Request) {
	if r == nil {
		return
	}

	for k, h := range r.Header {
		span.SetAttribute(tagHTTPRequestHeader(k), strings.Join(h, "\n"))
	}
	span.SetAttributes(semconv.HTTPClientAttributesFromHTTPRequest(r)...)
}

func tagsFromResponse(span trace.Span, r *http.Response) {
	if r == nil {
		return
	}

	for k, h := range r.Header {
		span.SetAttribute(tagHTTPResponseHeader(k), strings.Join(h, "\n"))
	}
	span.SetAttributes(semconv.HTTPResponseContentLengthKey.Int64(r.ContentLength))
	span.SetAttributes(semconv.HTTPAttributesFromHTTPStatusCode(r.StatusCode)...)
}

func initTracer(exporter exportTrace.SpanSyncer) error {
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
	tp, err := sdktrace.NewProvider(
		sdktrace.WithConfig(
			sdktrace.Config{
				DefaultSampler: sdktrace.AlwaysSample(),
			},
		),
		sdktrace.WithSyncer(exporter),
		sdktrace.WithResource(resource.New(
			semconv.ServiceNameKey.String("wnp-bridge"),
			semconv.ServiceInstanceIDKey.String(hostname),
			label.String("module.path", module),
			semconv.ServiceVersionKey.String(version),
		)),
	)
	if err != nil {
		return err
	}
	global.SetTraceProvider(tp)
	return nil
}
