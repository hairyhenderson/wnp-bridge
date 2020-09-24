package main

import (
	"net/http"

	"time"

	"github.com/povilasv/prommod"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	updateMetrics     = map[string]prometheus.ObserverVec{}
	clientObservers   = map[string]prometheus.ObserverVec{}
	clientGauges      = map[string]prometheus.Gauge{}
	clientCounterVecs = map[string]*prometheus.CounterVec{}
)

func initMetrics() {
	ns := "wnp_bridge"
	prometheus.MustRegister(prommod.NewCollector(ns), prometheus.NewBuildInfoCollector())

	// hue: Hue, sat: Saturation, val: Value/Brightness, on: On, acc: Accessory (identify event)
	for _, sub := range []string{"hue", "sat", "val", "on", "acc"} {
		updateMetrics[sub+"UpdateDurationHist"] = promauto.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: ns,
			Subsystem: sub,
			Name:      "update_duration_seconds",
			Buckets:   []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}, []string{"event"})
		updateMetrics[sub+"UpdateDurationSumm"] = promauto.NewSummaryVec(prometheus.SummaryOpts{
			Namespace:  ns,
			Subsystem:  sub,
			Name:       "update_duration_quantile_seconds",
			Objectives: map[float64]float64{0.1: 0.01, 0.5: 0.01, 0.9: 0.01, 0.99: 0.001, 0.999: 0.0001},
		}, []string{"event"})
	}

	initClientMetrics(ns)
}

func observeUpdateDuration(sub, event string, start time.Time) {
	diff := time.Since(start)
	l := prometheus.Labels{"event": event}
	updateMetrics[sub+"UpdateDurationHist"].With(l).Observe(diff.Seconds())
	updateMetrics[sub+"UpdateDurationSumm"].With(l).Observe(diff.Seconds())
}

func initClientMetrics(ns string) {
	sub := "client"
	clientGauges["clientInFlightGauge"] = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: ns,
		Subsystem: sub,
		Name:      "in_flight_requests",
		Help:      "A gauge of in-flight requests for the wrapped client.",
	})

	clientCounterVecs["clientRequestCounter"] = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Subsystem: sub,
			Name:      "requests_total",
			Help:      "A counter for requests from the wrapped client.",
		},
		[]string{"client", "code", "method"},
	)

	clientObservers = map[string]prometheus.ObserverVec{
		"traceDurationHist": promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: ns,
				Subsystem: sub,
				Name:      "request_phase_duration_seconds",
				Help:      "Trace duration histogram by phase",
				Buckets:   []float64{.005, .01, .025, .05},
			},
			[]string{"client", "phase"},
		),
		"traceDurationSumm": promauto.NewSummaryVec(
			prometheus.SummaryOpts{
				Namespace:  ns,
				Subsystem:  sub,
				Name:       "request_phase_duration_quantiles_seconds",
				Help:       "Trace duration summary by phase",
				Objectives: map[float64]float64{0.1: 0.1, 0.5: 0.05, 0.95: 0.01, 0.99: 0.001, 0.999: 0.0001},
			},
			[]string{"client", "phase"},
		),
		"clientDurationHist": promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: ns,
				Subsystem: sub,
				Name:      "request_duration_seconds",
				Help:      "A histogram of request latencies.",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"client", "method"},
		),
		"clientDurationSumm": promauto.NewSummaryVec(
			prometheus.SummaryOpts{
				Namespace:  ns,
				Subsystem:  sub,
				Name:       "request_duration_quantiles_seconds",
				Help:       "A summary of request latencies.",
				Objectives: map[float64]float64{0.1: 0.1, 0.5: 0.05, 0.95: 0.01, 0.99: 0.001, 0.999: 0.0001},
			},
			[]string{"client", "method"},
		),
	}
}

func instrumentHTTPClient(name string, rt http.RoundTripper) http.RoundTripper {
	l := prometheus.Labels{"client": name}
	return promhttp.InstrumentRoundTripperInFlight(clientGauges["clientInFlightGauge"],
		promhttp.InstrumentRoundTripperCounter(clientCounterVecs["clientRequestCounter"].MustCurryWith(l),
			promhttp.InstrumentRoundTripperTrace(instrumentHTTPClientTrace(name),
				promhttp.InstrumentRoundTripperDuration(clientObservers["clientDurationHist"].MustCurryWith(l),
					promhttp.InstrumentRoundTripperDuration(clientObservers["clientDurationSumm"].MustCurryWith(l),
						rt),
				),
			),
		),
	)
}

func instrumentHTTPClientTrace(name string) *promhttp.InstrumentTrace {
	observe := func(phase string) func(t float64) {
		l := prometheus.Labels{"client": name, "phase": phase}
		return func(t float64) {
			clientObservers["traceDurationHist"].With(l).Observe(t)
			clientObservers["traceDurationSumm"].With(l).Observe(t)
		}
	}

	return &promhttp.InstrumentTrace{
		GotConn:              observe("got_conn"),
		PutIdleConn:          observe("put_idle_conn"),
		GotFirstResponseByte: observe("got_first_response_byte"),
		Got100Continue:       observe("got_100_continue"),
		DNSStart:             observe("dns_start"),
		DNSDone:              observe("dns_done"),
		ConnectStart:         observe("connect_start"),
		ConnectDone:          observe("connect_done"),
		TLSHandshakeStart:    observe("tls_handshake_start"),
		TLSHandshakeDone:     observe("tls_handshake_done"),
		WroteHeaders:         observe("wrote_headers"),
		Wait100Continue:      observe("wait_100_continue"),
		WroteRequest:         observe("wrote_request"),
	}
}
