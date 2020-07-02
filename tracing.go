package main

import (
	"context"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel/api/global"
	"go.opentelemetry.io/otel/api/standard"
	"go.opentelemetry.io/otel/api/trace"
	exportTrace "go.opentelemetry.io/otel/sdk/export/trace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const (
	tagHTTPRequestContentLength  = "http.request.content_length"
	tagHTTPResponseContentLength = "http.response.content_length"
)

func tagHTTPRequestHeader(h string) string  { return "http.request.header." + h }
func tagHTTPResponseHeader(h string) string { return "http.response.header." + h }

func tagsFromRequest(span trace.Span, r *http.Request) {
	for k, h := range r.Header {
		span.SetAttribute(tagHTTPRequestHeader(k), strings.Join(h, "\n"))
	}
	span.SetAttributes(
		standard.HTTPClientAttributesFromHTTPRequest(r)...,
	)

	span.SetAttribute(tagHTTPRequestContentLength, r.ContentLength)
}

func tagsFromResponse(span trace.Span, r *http.Response) {
	for k, h := range r.Header {
		span.SetAttribute(tagHTTPResponseHeader(k), strings.Join(h, "\n"))
	}
	span.SetAttribute(tagHTTPResponseContentLength, r.ContentLength)
	span.SetAttributes(
		standard.HTTPStatusTextKey.String(r.Status),
		standard.HTTPStatusCodeKey.Int(r.StatusCode),
	)

	span.SetAttribute("error", r.StatusCode > 399)
}

func createSpan(ctx context.Context, operationName string) (context.Context, trace.Span) {
	tracer := global.TraceProvider().Tracer("wnp-bridge")
	spanOpts := []trace.StartOption{}
	parent := trace.SpanFromContext(ctx)
	if parent != nil {
		spanOpts = append(spanOpts, trace.LinkedTo(parent.SpanContext()))
	}
	return tracer.Start(ctx, operationName, spanOpts...)
}

func initTracer(exporter exportTrace.SpanSyncer) error {
	tp, err := sdktrace.NewProvider(
		sdktrace.WithConfig(
			sdktrace.Config{
				DefaultSampler: sdktrace.AlwaysSample(),
			},
		),
		sdktrace.WithSyncer(exporter),
	)
	if err != nil {
		return err
	}
	global.SetTraceProvider(tp)
	return nil
}
