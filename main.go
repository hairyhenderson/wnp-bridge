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

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otlpgrpc "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/brutella/hap"
	"github.com/brutella/hap/accessory"
	"github.com/brutella/hap/service"
	"github.com/hashicorp/mdns"
	"github.com/lucasb-eyer/go-colorful"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
)

func initTraceExporter(ctx context.Context, otlpEndpoint string) (closer func(context.Context) error, err error) {
	log := zerolog.Ctx(ctx)

	var exporter sdktrace.SpanExporter
	if otlpEndpoint == "" {
		exporter, err = stdouttrace.New(stdouttrace.WithWriter(log), stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, fmt.Errorf("failed to init stdout exporter: %w", err)
		}
	} else {
		exporter, err = otlpgrpc.New(ctx, otlpgrpc.WithEndpoint(otlpEndpoint), otlpgrpc.WithInsecure())
		if err != nil {
			return nil, fmt.Errorf("failed to init OTLP exporter: %w", err)
		}
	}

	return exporter.Shutdown, initTracer(ctx, exporter)
}

type opts struct {
	hostURL      string
	accName      string
	otlpEndpoint string
	storagePath  string
	pin          string
	addr         string
	metricsAddr  string
	enableIPv6   bool
	debug        bool
}

func parseFlags() opts {
	const (
		defaultPath = ""
		usage       = "storage path for HomeControl data"
	)

	o := opts{}

	flag.StringVar(&o.storagePath, "path", defaultPath, usage)
	flag.StringVar(&o.storagePath, "p", defaultPath, usage+" (shorthand)")
	flag.StringVar(&o.addr, "addr", "", "address to listen to")
	flag.StringVar(&o.metricsAddr, "metrics-addr", ":8080", "address to listen to for metrics")
	flag.StringVar(&o.hostURL, "host", "", "host URL for wifi neopixel device")
	flag.StringVar(&o.pin, "code", "12344321", "setup code")
	flag.StringVar(&o.accName, "name", "WiFi NeoPixel", "accessory name")
	flag.StringVar(&o.otlpEndpoint, "otlp-endpoint", "127.0.0.1:55680", "Endpoint for sending OTLP traces")
	flag.BoolVar(&o.enableIPv6, "enable-ipv6", false, "enable IPv6")
	flag.BoolVar(&o.debug, "debug", false, "Enable debug logging")

	flag.Parse()

	return o
}

func main() {
	o := parseFlags()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ctx, log := initLogger(ctx, o.debug)

	err := run(ctx, o)
	if err != nil {
		log.Error().Err(err).Msg("exiting with error")
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

	log.Debug().Msg("starting")

	initMetrics()

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{
		Addr:              o.metricsAddr,
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil {
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
		o.hostURL, err = mdnsLookup(ctx, "_neopixel._tcp", "local", o.enableIPv6)
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

	store := hap.NewFsStore(o.storagePath)

	t, err := hap.NewServer(store, acc.A)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to create transport: %w", err)
	}

	t.Pin = o.pin
	t.Addr = o.addr

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// End the init span before we start the HC transport
	span.End()

	log.Info().Str("accessory", o.accName).Str("setup_code", o.pin).Msg("starting up")

	return t.ListenAndServe(ctx)
}

func mdnsLookup(ctx context.Context, svc, domain string, enableIPv6 bool) (string, error) {
	log := zerolog.Ctx(ctx)
	_, span := otel.Tracer("").Start(ctx, "mDNS host lookup")
	defer span.End()

	hostURL := ""

	suffix := fmt.Sprintf("%s.%s.", svc, domain)
	// Make a channel for results and start listening
	entriesCh := make(chan *mdns.ServiceEntry, 4)
	go func() {
		for {
			select {
			case entry, ok := <-entriesCh:
				if !ok {
					return
				}

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
			case <-ctx.Done():
				return
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
		DisableIPv6:         !enableIPv6,
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
	span.SetAttributes(attribute.Float64Slice("hsv", []float64{h, s, v}))
	if err != nil {
		err = fmt.Errorf("strip.hsv failed while initializing light: %w", err)
		span.RecordError(err)
		return err
	}
	lb.Hue.SetValue(h)
	lb.Saturation.SetValue(s * 100)
	_ = lb.Brightness.SetValue(int(v * 100))
	return nil
}

func updateColor(ctx context.Context, lb *service.ColoredLightbulb, strip *wifineopixel) {
	tracer := otel.Tracer("")
	ctx, span := tracer.Start(ctx, "updateColor")
	defer span.End()
	log := zerolog.Ctx(ctx)

	h := lb.Hue.Value()
	s := lb.Saturation.Value() / 100
	v := float64(lb.Brightness.Value()) / 100

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

	lb.On.ValueRequestFunc = func(r *http.Request) (interface{}, int) {
		_, span := tracer.Start(ctx, "lb.On.ValueRequest")
		defer span.End()

		start := time.Now()
		log.Debug().Msg("lb.On.ValueRequest()")
		isOn := strip.isOn()
		observeUpdateDuration("on", "remoteGet", start)

		return isOn, 0
	}

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

	acc.IdentifyFunc = func(r *http.Request) {
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
	}
}
