package main

import (
	stdlog "log"
	"os"

	hclog "github.com/brutella/hc/log"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/ssh/terminal"
)

func initLogger() {
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	stdlogger := log.With().Bool("stdlog", true).Logger()
	stdlog.SetFlags(0)
	stdlog.SetOutput(stdlogger)

	if terminal.IsTerminal(int(os.Stdout.Fd())) {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})

		noLevelWriter := zerolog.ConsoleWriter{
			Out:         os.Stderr,
			FormatLevel: func(i interface{}) string { return "" },
		}
		stdlogger = stdlogger.Output(noLevelWriter)
		stdlog.SetOutput(stdlogger)
	}
	// Hook up HC's logging to zerolog
	hclog.Info.SetOutput(log.Logger)
}

// an adapter for logging from Jaeger to zerolog
type jlogger struct {
}

func (jlogger) Error(e string) {
	log.Error().Msg(e)
}

func (jlogger) Infof(msg string, args ...interface{}) {
	log.Debug().Msgf(msg, args...)
}
