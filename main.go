package main

import (
	"context"
	"flag"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/mdns"
	"github.com/lucasb-eyer/go-colorful"

	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/uber/jaeger-client-go"
	jaegercfg "github.com/uber/jaeger-client-go/config"
	jaegerprom "github.com/uber/jaeger-lib/metrics/prometheus"

	"github.com/brutella/hc"
	"github.com/brutella/hc/accessory"
	"github.com/brutella/hc/service"

	"github.com/rs/zerolog/log"
)

var (
	strip       *wifineopixel
	storagePath string
	hostURL     string
	setupCode   string
	accName     string
)

func init() {
	const (
		defaultPath = ""
		usage       = "storage path for HomeControl data"
	)
	flag.StringVar(&storagePath, "path", defaultPath, usage)
	flag.StringVar(&storagePath, "p", defaultPath, usage+" (shorthand)")
	flag.StringVar(&hostURL, "host", "", "host URL for wifi neopixel device")
	flag.StringVar(&setupCode, "code", "12344321", "setup code")
	flag.StringVar(&accName, "name", "WiFi NeoPixel", "accessory name")
}

func main() {
	flag.Parse()

	initLogger()
	initMetrics()

	go func() {
		http.Handle("/metrics", promhttp.Handler())
		if err := http.ListenAndServe(":8080", nil); err != nil {
			log.Error().Err(err).Send()
		}
	}()

	tracingCloser, err := initTracing("wnp-bridge")
	if err != nil {
		log.Fatal().Err(err).Msg("failed to init tracing")
	}
	defer tracingCloser.Close()

	ctx := context.Background()

	// lookup wifi neopixel by mDNS
	if hostURL == "" {
		// Make a channel for results and start listening
		entriesCh := make(chan *mdns.ServiceEntry, 4)
		go func() {
			for entry := range entriesCh {
				if strings.HasSuffix(entry.Name, "_neopixel._tcp.local.") {
					log.Info().Str("host", entry.Host).Str("name", entry.Name).IPAddr("addr", entry.Addr).Int("port", entry.Port).Msg("found neopixel")
					hostURL = "http://" + entry.Addr.String()
				}
			}
		}()

		// Start the lookup
		opts := &mdns.QueryParam{
			Timeout:             5 * time.Second,
			Domain:              "local",
			Service:             "_neopixel._tcp",
			Entries:             entriesCh,
			WantUnicastResponse: true,
		}
		err := mdns.Query(opts)
		if err != nil {
			log.Fatal().Err(err).Msg("")
		}
		close(entriesCh)
		if hostURL == "" {
			log.Fatal().Msg("neopixel not found")
		}
	}

	strip, err = newWifiNeopixel(ctx, hostURL)
	if err != nil {
		log.Fatal().Err(err).Msg("")
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

	initLight(ctx, lb, strip)

	updateColor := func(ctx context.Context, strip *wifineopixel) {
		span, ctx := createSpan(ctx, "updateColor")
		defer span.Finish()

		h := lb.Hue.GetValue()
		s := lb.Saturation.GetValue() / 100
		v := float64(lb.Brightness.GetValue()) / 100

		span.SetTag("hue", h)
		span.SetTag("sat", s)
		span.SetTag("val", v)

		log.Debug().Float64("hue", h).Float64("sat", s).Float64("val", v).Msg("updateColor")
		c := colorful.Hsv(h, s, float64(v))
		if err := strip.setSolid(ctx, c); err != nil {
			log.Error().Err(err).Msg("updateColor error")
		}
	}

	lb.Hue.OnValueRemoteUpdate(func(value float64) {
		span, ctx := createSpan(ctx, "lb.Hue.OnValueRemoteUpdate")
		defer span.Finish()
		span.SetTag("value", value)

		start := time.Now()
		log.Debug().Float64("hue", value).Msg("Changed Hue")
		updateColor(ctx, strip)
		observeUpdateDuration("hue", "remoteUpdate", start)
	})

	lb.Saturation.OnValueRemoteUpdate(func(value float64) {
		span, ctx := createSpan(ctx, "lb.Saturation.OnValueRemoteUpdate")
		defer span.Finish()
		span.SetTag("value", value)

		start := time.Now()
		log.Debug().Float64("sat", value).Msg("Changed Saturation")
		updateColor(ctx, strip)
		observeUpdateDuration("sat", "remoteUpdate", start)
	})

	lb.Brightness.OnValueRemoteUpdate(func(value int) {
		span, ctx := createSpan(ctx, "lb.Brightness.OnValueRemoteUpdate")
		defer span.Finish()
		span.SetTag("value", value)

		start := time.Now()
		log.Debug().Int("val", value).Msg("Changed Brightness")
		updateColor(ctx, strip)
		observeUpdateDuration("val", "remoteUpdate", start)
	})

	lb.On.OnValueRemoteGet(func() bool {
		span, _ := createSpan(ctx, "lb.On.OnValueRemoteGet")
		defer span.Finish()

		start := time.Now()
		log.Debug().Msg("lb.On.OnValueRemoteGet()")
		isOn := strip.isOn()
		observeUpdateDuration("on", "remoteGet", start)
		return isOn
	})

	lb.On.OnValueRemoteUpdate(func(on bool) {
		span, ctx := createSpan(ctx, "lb.On.OnValueRemoteUpdate")
		defer span.Finish()
		span.SetTag("value", on)

		start := time.Now()
		log.Debug().Bool("on", on).Msg("lb.On.OnValueRemoteUpdate")
		if on {
			strip.on(ctx)
		} else {
			strip.clear(ctx)
		}
		lb.On.SetValue(on)
		observeUpdateDuration("on", "remoteUpdate", start)
	})

	acc.OnIdentify(func() {
		span, ctx := createSpan(ctx, "acc.OnIdentify")
		defer span.Finish()

		start := time.Now()
		log.Debug().Msg("acc.OnIdentify()")
		initialOn := strip.isOn()
		if !initialOn {
			strip.on(ctx)
			time.Sleep(500 * time.Millisecond)
		}
		strip.clear(ctx)
		time.Sleep(500 * time.Millisecond)
		strip.on(ctx)
		time.Sleep(500 * time.Millisecond)
		strip.clear(ctx)
		time.Sleep(500 * time.Millisecond)
		strip.on(ctx)
		time.Sleep(500 * time.Millisecond)
		strip.clear(ctx)
		if initialOn {
			time.Sleep(500 * time.Millisecond)
			strip.on(ctx)
		}
		observeUpdateDuration("acc", "identify", start)
	})

	t, err := hc.NewIPTransport(hc.Config{
		Pin:         setupCode,
		StoragePath: storagePath,
	}, acc.Accessory)
	if err != nil {
		log.Fatal().Err(err).Send()
	}

	hc.OnTermination(func() {
		<-t.Stop()
	})

	log.Info().Msgf("starting up '%s'. setup code is %s", accName, setupCode)
	t.Start()
}

// initialize the HomeControl lightbulb service with the same values currently displaying on the WNP strip
func initLight(ctx context.Context, lb *service.ColoredLightbulb, strip *wifineopixel) {
	span, ctx := createSpan(ctx, "initLight")
	defer span.Finish()

	h, s, v, err := strip.hsv(ctx)
	span.SetTag("hsv", []float64{h, s, v})
	if err != nil {
		ext.Error.Set(span, true)
		span.SetTag("err", err)
		log.Fatal().Err(err).Msg("")
	}
	lb.Hue.SetValue(h)
	lb.Saturation.SetValue(s * 100)
	lb.Brightness.SetValue(int(v * 100))
}

func initTracing(name string) (io.Closer, error) {
	cfg := jaegercfg.Configuration{
		ServiceName: name,
		Sampler: &jaegercfg.SamplerConfig{
			Type:  jaeger.SamplerTypeRemote,
			Param: 1,
		},
		Reporter: &jaegercfg.ReporterConfig{
			LogSpans: true,
		},
	}

	_, err := cfg.FromEnv()
	if err != nil {
		return nil, err
	}

	// Initialize tracer with a logger and a metrics factory
	tracer, closer, err := cfg.NewTracer(
		jaegercfg.Logger(jlogger{}),
		jaegercfg.Metrics(jaegerprom.New()),
	)
	if err != nil {
		return nil, err
	}
	// Set the singleton opentracing.Tracer with the Jaeger tracer.
	opentracing.SetGlobalTracer(tracer)
	return closer, nil
}
