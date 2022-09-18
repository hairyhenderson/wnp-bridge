package main

import (
	"context"
	stdlog "log"
	"os"

	hclog "github.com/brutella/hap/log"
	"github.com/go-logr/zerologr"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel"
	"golang.org/x/term"
)

func initLogger(ctx context.Context, debug bool) (context.Context, zerolog.Logger) {
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	stdlogger := log.With().Str("component", "stdout").Logger()
	stdlog.SetFlags(0)
	stdlog.SetOutput(stdlogger)

	if term.IsTerminal(int(os.Stdout.Fd())) {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})

		noLevelWriter := zerolog.ConsoleWriter{
			Out:         os.Stderr,
			FormatLevel: func(i interface{}) string { return "" },
		}
		stdlogger = stdlogger.Output(noLevelWriter)
		stdlog.SetOutput(stdlogger)
	}

	hapLog := log.With().Str("component", "hap").Logger()

	if debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)

		hclog.Debug.SetOutput(&hapLog)
	}
	hclog.Info.SetOutput(&hapLog)

	// otel logs should be sent to the same logger as the rest of the app
	otelLog := log.With().Str("component", "otel").Logger()
	otel.SetLogger(zerologr.New(&otelLog))
	// errors from the exporter should be logged
	otel.SetErrorHandler(&zlErrorHandler{&otelLog})

	ctx = log.Logger.WithContext(ctx)
	return ctx, log.Logger
}

// zlErrorHandler logs Otel errors with a Zerolog logger
type zlErrorHandler struct {
	l *zerolog.Logger
}

// Handle logs err if no delegate is set, otherwise it is delegated.
func (h *zlErrorHandler) Handle(err error) {
	h.l.Error().Err(err).Send()
}
