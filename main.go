package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpgrpc"
	"go.opentelemetry.io/otel/exporters/stdout"
	exportTrace "go.opentelemetry.io/otel/sdk/export/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/hashicorp/mdns"
	"github.com/lucasb-eyer/go-colorful"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/brutella/hc"
	"github.com/brutella/hc/accessory"
	hclog "github.com/brutella/hc/log"
	"github.com/brutella/hc/service"

	"github.com/rs/zerolog"
)

func initTraceExporter(ctx context.Context, otlpEndpoint string) (closer func(context.Context) error, err error) {
	log := zerolog.Ctx(ctx)

	var exporter exportTrace.SpanExporter
	if otlpEndpoint == "" {
		exporter, err = stdout.NewExporter(stdout.WithWriter(log), stdout.WithPrettyPrint())
		if err != nil {
			return nil, fmt.Errorf("failed to init stdout exporter: %w", err)
		}
	} else {
		driver := otlpgrpc.NewDriver(otlpgrpc.WithEndpoint(otlpEndpoint), otlpgrpc.WithInsecure())
		exporter, err = otlp.NewExporter(ctx, driver)
		if err != nil {
			return nil, fmt.Errorf("failed to init OTLP exporter: %w", err)
		}
	}

	return exporter.Shutdown, initTracer(exporter)
}

type opts struct {
	hostURL      string
	accName      string
	otlpEndpoint string
	debug        bool

	config hc.Config
}

func parseFlags() opts {
	const (
		defaultPath = ""
		usage       = "storage path for HomeControl data"
	)
	addr := ""

	o := opts{}

	flag.StringVar(&o.config.StoragePath, "path", defaultPath, usage)
	flag.StringVar(&o.config.StoragePath, "p", defaultPath, usage+" (shorthand)")
	flag.StringVar(&addr, "addr", "", "address to listen to")
	flag.StringVar(&o.hostURL, "host", "", "host URL for wifi neopixel device")
	flag.StringVar(&o.config.Pin, "code", "12344321", "setup code")
	flag.StringVar(&o.accName, "name", "WiFi NeoPixel", "accessory name")
	flag.StringVar(&o.otlpEndpoint, "otlp-endpoint", "localhost:55680", "Endpoint for sending OTLP traces")
	flag.BoolVar(&o.debug, "debug", false, "Enable debug logging")

	flag.Parse()

	parts := strings.SplitN(addr, ":", 2)
	//nolint:staticcheck
	o.config.IP = parts[0]
	o.config.Port = ""
	if len(parts) == 2 {
		o.config.Port = parts[1]
	}

	return o
}

func main() {
	o := parseFlags()
	ctx := context.Background()
	ctx, log := initLogger(ctx)
	if o.debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
		hclog.Debug.Enable()
	}

	err := run(ctx, o)
	if err != nil {
		log.Fatal().Err(err).Send()
	}
}

func run(ctx context.Context, o opts) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	log := zerolog.Ctx(ctx)

	closer, err := initTraceExporter(ctx, o.otlpEndpoint)
	if err != nil {
		return fmt.Errorf("failed to init tracing: %w", err)
	}
	//nolint:errcheck
	defer closer(ctx)

	initMetrics()
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		if err := http.ListenAndServe(":8080", mux); err != nil {
			log.Error().Err(err).Send()
		}
	}()

	tracer := otel.Tracer("")
	// provide a different context so that triggered spans aren't children of
	// this one
	initCtx, span := tracer.Start(ctx, "init")
	defer span.End()

	// lookup wifi neopixel by mDNS
	if o.hostURL == "" {
		o.hostURL, err = mdnsLookup(ctx, "_neopixel._tcp", "local")
		if err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to init mDNS: %w", err)
		}
	}

	strip, err := newWifiNeopixel(initCtx, o.hostURL)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to init WiFiNeopixel: %w", err)
	}

	info := accessory.Info{
		Name:         o.accName,
		SerialNumber: "0123456789",
		Model:        "a",
		// FirmwareRevision:
		Manufacturer: "Dave Henderson",
	}

	acc := accessory.NewColoredLightbulb(info)
	lb := acc.Lightbulb

	err = initLight(initCtx, lb, strip)
	if err != nil {
		return err
	}

	initResponders(ctx, acc, strip)

	t, err := hc.NewIPTransport(o.config, acc.Accessory)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to create transport: %w", err)
	}

	go func(ctx context.Context) {
		<-ctx.Done()
		zerolog.Ctx(ctx).Error().Err(ctx.Err()).Msg("context done")
		<-t.Stop()
	}(ctx)

	// End the init span before we start the HC transport
	span.End()

	log.Info().Msgf("starting up '%s'. setup code is %s", o.accName, o.config.Pin)
	t.Start()
	return nil
}

func mdnsLookup(ctx context.Context, svc, domain string) (string, error) {
	log := zerolog.Ctx(ctx)
	_, span := otel.Tracer("").Start(ctx, "mDNS host lookup")
	defer span.End()

	hostURL := ""

	suffix := fmt.Sprintf("%s.%s.", svc, domain)
	// Make a channel for results and start listening
	entriesCh := make(chan *mdns.ServiceEntry, 4)
	go func() {
		for entry := range entriesCh {
			if strings.HasSuffix(entry.Name, suffix) {
				log.Info().Str("host", entry.Host).Str("name", entry.Name).IPAddr("addr", entry.Addr).Int("port", entry.Port).Msg("found neopixel")
				span.AddEvent("mDNS: got entry",
					trace.WithAttributes(
						attribute.String("entry.host", entry.Host),
						attribute.String("entry.name", entry.Name),
						attribute.Stringer("entry.addr", entry.Addr),
						attribute.Int("entry.port", entry.Port),
					))
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
func initLight(ctx context.Context, lb *service.ColoredLightbulb, strip *wifineopixel) error {
	ctx, span := otel.Tracer("").Start(ctx, "initLight")
	defer span.End()

	h, s, v, err := strip.hsv(ctx)
	span.SetAttributes(attribute.Array("hsv", []float64{h, s, v}))
	if err != nil {
		err = fmt.Errorf("strip.hsv failed while initializing light: %w", err)
		span.RecordError(err)
		return err
	}
	lb.Hue.SetValue(h)
	lb.Saturation.SetValue(s * 100)
	lb.Brightness.SetValue(int(v * 100))
	return nil
}

func updateColor(ctx context.Context, lb *service.ColoredLightbulb, strip *wifineopixel) {
	tracer := otel.Tracer("")
	ctx, span := tracer.Start(ctx, "updateColor")
	defer span.End()
	log := zerolog.Ctx(ctx)

	h := lb.Hue.GetValue()
	s := lb.Saturation.GetValue() / 100
	v := float64(lb.Brightness.GetValue()) / 100

	span.SetAttributes(
		attribute.Float64("hue", h),
		attribute.Float64("sat", s),
		attribute.Float64("val", v),
	)

	log.Debug().Float64("hue", h).Float64("sat", s).Float64("val", v).Msg("updateColor")
	c := colorful.Hsv(h, s, v)
	if err := strip.setSolid(ctx, c); err != nil {
		err = fmt.Errorf("updateColor failed: %w", err)
		log.Error().Err(err).Send()
		span.RecordError(err)
	}
}

func initResponders(ctx context.Context, acc *accessory.ColoredLightbulb, strip *wifineopixel) {
	lb := acc.Lightbulb
	tracer := otel.Tracer("")

	log := zerolog.Ctx(ctx)

	lb.Hue.OnValueRemoteUpdate(func(value float64) {
		ctx, span := tracer.Start(ctx, "lb.Hue.OnValueRemoteUpdate")
		defer span.End()
		span.SetAttributes(attribute.Float64("value", value))

		start := time.Now()
		log.Debug().Float64("hue", value).Msg("Changed Hue")
		updateColor(ctx, lb, strip)
		observeUpdateDuration("hue", "remoteUpdate", start)
	})

	lb.Saturation.OnValueRemoteUpdate(func(value float64) {
		ctx, span := tracer.Start(ctx, "lb.Saturation.OnValueRemoteUpdate")
		defer span.End()
		span.SetAttributes(attribute.Float64("value", value))

		start := time.Now()
		log.Debug().Float64("sat", value).Msg("Changed Saturation")
		updateColor(ctx, lb, strip)
		observeUpdateDuration("sat", "remoteUpdate", start)
	})

	lb.Brightness.OnValueRemoteUpdate(func(value int) {
		ctx, span := tracer.Start(ctx, "lb.Brightness.OnValueRemoteUpdate")
		defer span.End()
		span.SetAttributes(attribute.Int("value", value))

		start := time.Now()
		log.Debug().Int("val", value).Msg("Changed Brightness")
		updateColor(ctx, lb, strip)
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
		span.SetAttributes(attribute.Bool("value", on))

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
