package main

import (
	"flag"
	"strings"
	"time"

	"github.com/hashicorp/mdns"
	"github.com/lucasb-eyer/go-colorful"

	"github.com/brutella/hc"
	"github.com/brutella/hc/accessory"
	"github.com/brutella/hc/service"

	"github.com/rs/zerolog/log"
)

var (
	strip       *wifineopixel
	storagePath string
	hostURL     string
)

func init() {
	const (
		defaultPath = ""
		usage       = "storage path for HomeControl data"
	)
	flag.StringVar(&storagePath, "path", defaultPath, usage)
	flag.StringVar(&storagePath, "p", defaultPath, usage+" (shorthand)")
	flag.StringVar(&hostURL, "host", "", "host URL for wifi neopixel device")
}

func main() {
	flag.Parse()

	initLogger()

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

	var err error
	strip, err = newWifiNeopixel(hostURL)
	if err != nil {
		log.Fatal().Err(err).Msg("")
	}

	info := accessory.Info{
		Name:         "WiFi NeoPixel",
		SerialNumber: "0123456789",
		Model:        "a",
		// FirmwareRevision:
		Manufacturer: "Dave Henderson",
	}

	acc := accessory.NewLightbulb(info)
	lb := acc.Lightbulb

	initLight(lb, strip)

	updateColor := func(strip *wifineopixel) {
		h := lb.Hue.GetValue()
		s := lb.Saturation.GetValue() / 100
		v := float64(lb.Brightness.GetValue()) / 100
		log.Debug().Float64("hue", h).Float64("sat", s).Float64("val", v).Msg("updateColor")
		c := colorful.Hsv(h, s, float64(v))
		if err := strip.setSolid(c); err != nil {
			log.Error().Err(err).Msg("updateColor error")
		}
	}

	lb.Hue.OnValueRemoteUpdate(func(value float64) {
		log.Debug().Float64("hue", value).Msg("Changed Hue")
		updateColor(strip)
	})

	lb.Saturation.OnValueRemoteUpdate(func(value float64) {
		log.Debug().Float64("sat", value).Msg("Changed Saturation")
		updateColor(strip)
	})

	lb.Brightness.OnValueRemoteUpdate(func(value int) {
		log.Debug().Int("val", value).Msg("Changed Brightness")
		updateColor(strip)
	})

	lb.On.OnValueRemoteGet(func() bool {
		log.Debug().Msg("lb.On.OnValueRemoteGet()")
		return strip.isOn()
	})

	lb.On.OnValueRemoteUpdate(func(on bool) {
		log.Debug().Bool("on", on).Msg("lb.On.OnValueRemoteUpdate")
		if on {
			strip.on()
		} else {
			strip.clear()
		}
		lb.On.SetValue(on)
	})

	acc.OnIdentify(func() {
		log.Debug().Msg("acc.OnIdentify()")
		initialOn := strip.isOn()
		if !initialOn {
			strip.on()
			time.Sleep(500 * time.Millisecond)
		}
		strip.clear()
		time.Sleep(500 * time.Millisecond)
		strip.on()
		time.Sleep(500 * time.Millisecond)
		strip.clear()
		time.Sleep(500 * time.Millisecond)
		strip.on()
		time.Sleep(500 * time.Millisecond)
		strip.clear()
		if initialOn {
			time.Sleep(500 * time.Millisecond)
			strip.on()
		}
	})

	t, err := hc.NewIPTransport(hc.Config{
		Pin:         "12344321",
		StoragePath: storagePath,
	}, acc.Accessory)
	if err != nil {
		log.Fatal().Err(err).Msg("")
	}

	hc.OnTermination(func() {
		<-t.Stop()
	})

	t.Start()
}

// initialize the HomeControl lightbulb service with the same values currently displaying on the WNP strip
func initLight(lb *service.Lightbulb, strip *wifineopixel) {
	h, s, v, err := strip.hsv()
	if err != nil {
		log.Fatal().Err(err).Msg("")
	}
	lb.Hue.SetValue(h)
	lb.Saturation.SetValue(s * 100)
	lb.Brightness.SetValue(int(v * 100))
}
