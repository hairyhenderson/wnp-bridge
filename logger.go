package main

import (
	"context"
	stdlog "log"
	"os"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/ssh/terminal"
)

func initLogger(ctx context.Context) (context.Context, zerolog.Logger) {
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

	ctx = log.Logger.WithContext(ctx)
	return ctx, log.Logger
}
