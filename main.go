package main

import (
	"context"
	"flag"
	"net/http"
	"strings"
	"time"

	"github.com/honeycombio/opentelemetry-exporter-go/honeycomb"
	"go.opentelemetry.io/otel/exporters/stdout"
	exportTrace "go.opentelemetry.io/otel/sdk/export/trace"

	"github.com/hashicorp/mdns"
	"github.com/lucasb-eyer/go-colorful"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/brutella/hc"
	"github.com/brutella/hc/accessory"
	hclog "github.com/brutella/hc/log"
	"github.com/brutella/hc/service"

	"github.com/rs/zerolog/log"
)

var (
	strip       *wifineopixel
	storagePath string
	hostURL     string
	setupCode   string
	accName     string
	honeyKey    string
	honeyDS     string
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

	flag.StringVar(&honeyKey, "honeycomb-api-key", "", "API key for sending trace data to HoneyComb")
	flag.StringVar(&honeyDS, "honeycomb-dataset", "wnp-bridge", "Targeting Dataset for HoneyComb")
}

func main() {
	flag.Parse()

	ctx, log := initLogger(context.Background())
	// Hook up HC's logging to zerolog
	hclog.Info.SetOutput(log)

	initMetrics()

	go func() {
		http.Handle("/metrics", promhttp.Handler())
		if err := http.ListenAndServe(":8080", nil); err != nil {
			log.Error().Err(err).Send()
		}
	}()

	var exporter exportTrace.SpanSyncer
	if honeyKey == "" {
		exporter, _ = stdout.NewExporter(stdout.WithWriter(log), stdout.WithPrettyPrint())
	} else {
		hcExporter, err := honeycomb.NewExporter(
			honeycomb.Config{
				APIKey: honeyKey,
			},
			honeycomb.TargetingDataset(honeyDS),
			honeycomb.WithServiceName("wnp-bridge"),
			honeycomb.WithDebugEnabled())
		if err != nil {
			log.Fatal().Err(err).Msg("failed to init honeycomb exporter")
		}
		defer hcExporter.Close()
		exporter = hcExporter
	}

	err := initTracer(exporter)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to init tracing")
	}

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
		ctx, span := createSpan(ctx, "updateColor")
		defer span.End()

		h := lb.Hue.GetValue()
		s := lb.Saturation.GetValue() / 100
		v := float64(lb.Brightness.GetValue()) / 100

		span.SetAttribute("hue", h)
		span.SetAttribute("sat", s)
		span.SetAttribute("val", v)

		log.Debug().Float64("hue", h).Float64("sat", s).Float64("val", v).Msg("updateColor")
		c := colorful.Hsv(h, s, float64(v))
		if err := strip.setSolid(ctx, c); err != nil {
			log.Error().Err(err).Msg("updateColor error")
		}
	}

	lb.Hue.OnValueRemoteUpdate(func(value float64) {
		ctx, span := createSpan(ctx, "lb.Hue.OnValueRemoteUpdate")
		defer span.End()
		span.SetAttribute("value", value)

		start := time.Now()
		log.Debug().Float64("hue", value).Msg("Changed Hue")
		updateColor(ctx, strip)
		observeUpdateDuration("hue", "remoteUpdate", start)
	})

	lb.Saturation.OnValueRemoteUpdate(func(value float64) {
		ctx, span := createSpan(ctx, "lb.Saturation.OnValueRemoteUpdate")
		defer span.End()
		span.SetAttribute("value", value)

		start := time.Now()
		log.Debug().Float64("sat", value).Msg("Changed Saturation")
		updateColor(ctx, strip)
		observeUpdateDuration("sat", "remoteUpdate", start)
	})

	lb.Brightness.OnValueRemoteUpdate(func(value int) {
		ctx, span := createSpan(ctx, "lb.Brightness.OnValueRemoteUpdate")
		defer span.End()
		span.SetAttribute("value", value)

		start := time.Now()
		log.Debug().Int("val", value).Msg("Changed Brightness")
		updateColor(ctx, strip)
		observeUpdateDuration("val", "remoteUpdate", start)
	})

	lb.On.OnValueRemoteGet(func() bool {
		_, span := createSpan(ctx, "lb.On.OnValueRemoteGet")
		defer span.End()

		start := time.Now()
		log.Debug().Msg("lb.On.OnValueRemoteGet()")
		isOn := strip.isOn()
		observeUpdateDuration("on", "remoteGet", start)
		return isOn
	})

	lb.On.OnValueRemoteUpdate(func(on bool) {
		ctx, span := createSpan(ctx, "lb.On.OnValueRemoteUpdate")
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
		ctx, span := createSpan(ctx, "acc.OnIdentify")
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
	ctx, span := createSpan(ctx, "initLight")
	defer span.End()

	h, s, v, err := strip.hsv(ctx)
	span.SetAttribute("hsv", []float64{h, s, v})
	if err != nil {
		span.RecordError(ctx, err)
		log.Fatal().Err(err).Msg("")
	}
	lb.Hue.SetValue(h)
	lb.Saturation.SetValue(s * 100)
	lb.Brightness.SetValue(int(v * 100))
}
