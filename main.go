package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/api/global"
	"go.opentelemetry.io/otel/api/trace"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp"
	"go.opentelemetry.io/otel/exporters/stdout"
	"go.opentelemetry.io/otel/label"
	exportTrace "go.opentelemetry.io/otel/sdk/export/trace"

	"github.com/hashicorp/mdns"
	"github.com/lucasb-eyer/go-colorful"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/brutella/hc"
	"github.com/brutella/hc/accessory"
	"github.com/brutella/hc/service"

	"github.com/rs/zerolog"
)

func initTraceExporter(log zerolog.Logger, otlpEndpoint string) (closer func(context.Context) error, err error) {
	var exporter exportTrace.SpanExporter
	if otlpEndpoint == "" {
		exporter, err = stdout.NewExporter(stdout.WithWriter(log), stdout.WithPrettyPrint())
		if err != nil {
			return nil, fmt.Errorf("failed to init stdout exporter: %w", err)
		}
	} else {
		exporter, err = otlp.NewExporter(
			otlp.WithAddress(otlpEndpoint),
			otlp.WithInsecure(),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to init OTLP exporter: %w", err)
		}
	}

	return exporter.Shutdown, initTracer(exporter)
}

func main() {
	var (
		storagePath  string
		hostURL      string
		setupCode    string
		accName      string
		otlpEndpoint string
		debug        bool
	)
	const (
		defaultPath = ""
		usage       = "storage path for HomeControl data"
	)

	flag.StringVar(&storagePath, "path", defaultPath, usage)
	flag.StringVar(&storagePath, "p", defaultPath, usage+" (shorthand)")
	flag.StringVar(&hostURL, "host", "", "host URL for wifi neopixel device")
	flag.StringVar(&setupCode, "code", "12344321", "setup code")
	flag.StringVar(&accName, "name", "WiFi NeoPixel", "accessory name")
	flag.StringVar(&otlpEndpoint, "otlp-endpoint", "localhost:55680", "Endpoint for sending OTLP traces")
	flag.BoolVar(&debug, "debug", false, "Enable debug logging")

	flag.Parse()

	ctx, mainCancel := context.WithCancel(context.Background())
	defer mainCancel()

	ctx, log := initLogger(ctx)
	if debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}

	closer, err := initTraceExporter(log, otlpEndpoint)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to init tracing")
	}
	defer closer(ctx)

	initMetrics()
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		if err := http.ListenAndServe(":8080", mux); err != nil {
			log.Error().Err(err).Send()
		}
	}()

	tracer := global.Tracer("")
	// provide a different context so that triggered spans aren't children of
	// this one
	initCtx, span := tracer.Start(ctx, "init")
	defer span.End()

	// lookup wifi neopixel by mDNS
	if hostURL == "" {
		hostURL, err = mdnsLookup(ctx, "_neopixel._tcp", "local")
		if err != nil {
			span.RecordError(ctx, err, trace.WithErrorStatus(codes.Unknown))
			log.Fatal().Err(err).Send()
		}
	}

	strip, err := newWifiNeopixel(initCtx, hostURL)
	if err != nil {
		span.RecordError(initCtx, err, trace.WithErrorStatus(codes.Unknown))
		log.Fatal().Err(err).Send()
	}

	info := accessory.Info{
		Name:         accName,
		SerialNumber: "0123456789",
		Model:        "a",
		// FirmwareRevision:
		Manufacturer: "Dave Henderson",
	}

	acc := accessory.NewColoredLightbulb(info)
	lb := acc.Lightbulb

	initLight(initCtx, lb, strip)

	initResponders(ctx, acc, strip)

	t, err := hc.NewIPTransport(hc.Config{
		Pin:         setupCode,
		StoragePath: storagePath,
	}, acc.Accessory)
	if err != nil {
		span.RecordError(initCtx, err, trace.WithErrorStatus(codes.Unknown))
		log.Fatal().Err(err).Send()
	}

	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt)
	signal.Notify(c, syscall.SIGTERM)

	go func(ctx context.Context) {
		select {
		case sig := <-c:
			zerolog.Ctx(ctx).Error().Stringer("signal", sig).Msg("terminating due to signal")
			<-t.Stop()
		case <-ctx.Done():
			zerolog.Ctx(ctx).Error().Err(ctx.Err()).Msg("context done")
			<-t.Stop()
		}
	}(ctx)

	// End the init span before we start the HC transport
	span.End()

	log.Info().Msgf("starting up '%s'. setup code is %s", accName, setupCode)
	t.Start()
}

func mdnsLookup(ctx context.Context, svc, domain string) (string, error) {
	log := zerolog.Ctx(ctx)
	ctx, span := global.Tracer("").Start(ctx, "mDNS host lookup")
	defer span.End()

	hostURL := ""

	suffix := fmt.Sprintf("%s.%s.", svc, domain)
	// Make a channel for results and start listening
	entriesCh := make(chan *mdns.ServiceEntry, 4)
	go func() {
		for entry := range entriesCh {
			if strings.HasSuffix(entry.Name, suffix) {
				log.Info().Str("host", entry.Host).Str("name", entry.Name).IPAddr("addr", entry.Addr).Int("port", entry.Port).Msg("found neopixel")
				span.AddEvent(ctx, "mDNS: got entry",
					label.String("entry.host", entry.Host),
					label.String("entry.name", entry.Name),
					label.Stringer("entry.addr", entry.Addr),
					label.Int("entry.port", entry.Port),
				)
				hostURL = "http://" + entry.Addr.String()
			}
		}
	}()

	// Start the lookup
	opts := &mdns.QueryParam{
		Timeout:             5 * time.Second,
		Domain:              domain,
		Service:             svc,
		Entries:             entriesCh,
		WantUnicastResponse: true,
	}
	err := mdns.Query(opts)
	close(entriesCh)
	if err != nil {
		err = fmt.Errorf("neopixel not found: %w", err)
	} else if hostURL == "" {
		err = fmt.Errorf("neopixel not found")
	}
	return hostURL, err
}

// initialize the HomeControl lightbulb service with the same values currently displaying on the WNP strip
func initLight(ctx context.Context, lb *service.ColoredLightbulb, strip *wifineopixel) {
	ctx, span := global.Tracer("").Start(ctx, "initLight")
	defer span.End()
	log := zerolog.Ctx(ctx)

	h, s, v, err := strip.hsv(ctx)
	span.SetAttribute("hsv", []float64{h, s, v})
	if err != nil {
		err = fmt.Errorf("strip.hsv failed while initializing light: %w", err)
		span.RecordError(ctx, err)
		log.Fatal().Err(err).Send()
	}
	lb.Hue.SetValue(h)
	lb.Saturation.SetValue(s * 100)
	lb.Brightness.SetValue(int(v * 100))
}

func initResponders(ctx context.Context, acc *accessory.ColoredLightbulb, strip *wifineopixel) {
	lb := acc.Lightbulb
	tracer := global.Tracer("")

	updateColor := func(ctx context.Context, strip *wifineopixel) {
		ctx, span := tracer.Start(ctx, "updateColor")
		defer span.End()
		log := zerolog.Ctx(ctx)

		h := lb.Hue.GetValue()
		s := lb.Saturation.GetValue() / 100
		v := float64(lb.Brightness.GetValue()) / 100

		span.SetAttribute("hue", h)
		span.SetAttribute("sat", s)
		span.SetAttribute("val", v)

		log.Debug().Float64("hue", h).Float64("sat", s).Float64("val", v).Msg("updateColor")
		c := colorful.Hsv(h, s, float64(v))
		if err := strip.setSolid(ctx, c); err != nil {
			err = fmt.Errorf("updateColor failed: %w", err)
			log.Error().Err(err).Send()
			span.RecordError(ctx, err, trace.WithErrorStatus(codes.Unknown))
		}
	}

	log := zerolog.Ctx(ctx)

	lb.Hue.OnValueRemoteUpdate(func(value float64) {
		ctx, span := tracer.Start(ctx, "lb.Hue.OnValueRemoteUpdate")
		defer span.End()
		span.SetAttribute("value", value)

		start := time.Now()
		log.Debug().Float64("hue", value).Msg("Changed Hue")
		updateColor(ctx, strip)
		observeUpdateDuration("hue", "remoteUpdate", start)
	})

	lb.Saturation.OnValueRemoteUpdate(func(value float64) {
		ctx, span := tracer.Start(ctx, "lb.Saturation.OnValueRemoteUpdate")
		defer span.End()
		span.SetAttribute("value", value)

		start := time.Now()
		log.Debug().Float64("sat", value).Msg("Changed Saturation")
		updateColor(ctx, strip)
		observeUpdateDuration("sat", "remoteUpdate", start)
	})

	lb.Brightness.OnValueRemoteUpdate(func(value int) {
		ctx, span := tracer.Start(ctx, "lb.Brightness.OnValueRemoteUpdate")
		defer span.End()
		span.SetAttribute("value", value)

		start := time.Now()
		log.Debug().Int("val", value).Msg("Changed Brightness")
		updateColor(ctx, strip)
		observeUpdateDuration("val", "remoteUpdate", start)
	})

	lb.On.OnValueRemoteGet(func() bool {
		_, span := tracer.Start(ctx, "lb.On.OnValueRemoteGet")
		defer span.End()

		start := time.Now()
		log.Debug().Msg("lb.On.OnValueRemoteGet()")
		isOn := strip.isOn()
		observeUpdateDuration("on", "remoteGet", start)
		return isOn
	})

	lb.On.OnValueRemoteUpdate(func(on bool) {
		ctx, span := tracer.Start(ctx, "lb.On.OnValueRemoteUpdate")
		defer span.End()
		span.SetAttribute("value", on)

		start := time.Now()
		log.Debug().Bool("on", on).Msg("lb.On.OnValueRemoteUpdate")
		var err error
		if on {
			err = strip.on(ctx)
		} else {
			err = strip.clear(ctx)
		}
		if err != nil {
			log.Error().Err(err).Bool("on", on).Msg("error during lb.On.OnValueRemoteUpdate")
		}
		lb.On.SetValue(on)
		observeUpdateDuration("on", "remoteUpdate", start)
	})

	acc.OnIdentify(func() {
		ctx, span := tracer.Start(ctx, "acc.OnIdentify")
		defer span.End()

		start := time.Now()
		log.Debug().Msg("acc.OnIdentify()")
		var err error
		initialOn := strip.isOn()
		if !initialOn {
			err = strip.on(ctx)
			if err != nil {
				log.Error().Err(err).Bool("initialOn", initialOn).Msg("error during acc.OnIdentify")
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		for i := 0; i < 4; i++ {
			if i%2 == 0 {
				err = strip.clear(ctx)
			} else {
				err = strip.on(ctx)
			}
			if err != nil {
				log.Error().Err(err).Bool("initialOn", initialOn).Int("i", i).Msg("error during acc.OnIdentify blinking")
				return
			}
			time.Sleep(500 * time.Millisecond)
		}

		if initialOn {
			time.Sleep(500 * time.Millisecond)
			err = strip.on(ctx)
			if err != nil {
				log.Error().Err(err).Bool("initialOn", initialOn).Msg("error during acc.OnIdentify")
				return
			}
		}
		observeUpdateDuration("acc", "identify", start)
	})
}
