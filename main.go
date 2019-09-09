package main

import (
	"log"

	"github.com/lucasb-eyer/go-colorful"

	"github.com/brutella/hc"
	"github.com/brutella/hc/accessory"
)

func turnLightOn() {
	log.Println("Turn Light On")
}

func turnLightOff() {
	log.Println("Turn Light Off")
}

var (
	strip *wifineopixel
)

func main() {
	var err error
	strip, err = newWifiNeopixel("http://10.0.1.141")
	if err != nil {
		log.Fatal(err)
	}

	info := accessory.Info{
		Name:         "WiFi NeoPixel",
		Manufacturer: "Dave Henderson",
	}

	acc := accessory.NewLightbulb(info)
	lb := acc.Lightbulb

	updateColor := func(strip *wifineopixel) {
		h := lb.Hue.GetValue()
		s := lb.Saturation.GetValue()
		v := lb.Brightness.GetValue()
		c := colorful.Hsv(h, s, float64(v))
		if err := strip.setSolid(c); err != nil {
			log.Printf("updateColor error: %v\n", err)
		}
	}

	lb.Hue.OnValueRemoteGet(func() float64 {
		h, _, _, err := strip.hsv()
		if err != nil {
			log.Printf("error: %v\n", err)
			return 0
		}
		return h
	})

	lb.Saturation.OnValueRemoteGet(func() float64 {
		_, s, _, err := strip.hsv()
		if err != nil {
			log.Printf("error: %v\n", err)
			return 0
		}
		return s
	})

	lb.Brightness.OnValueRemoteGet(func() int {
		_, _, v, err := strip.hsv()
		if err != nil {
			log.Printf("error: %v\n", err)
			return 0
		}
		return int(v)
	})

	lb.Hue.OnValueRemoteUpdate(func(value float64) {
		log.Printf("Changed Hue to %f", value)
		updateColor(strip)
	})

	lb.Saturation.OnValueRemoteUpdate(func(value float64) {
		log.Printf("Changed Saturation to %f", value)
		updateColor(strip)
	})

	lb.Brightness.OnValueRemoteUpdate(func(value int) {
		log.Printf("Changed Brightness to %d", value)
		updateColor(strip)
	})

	lb.On.OnValueRemoteGet(func() bool {
		log.Println("lb.On.OnValueRemoteGet()")
		return strip.isOn()
	})

	lb.On.OnValueRemoteUpdate(func(on bool) {
		log.Printf("lb.On.OnValueRemoteUpdate(%v)\n", on)
		if on == true {
			strip.on()
		} else {
			strip.clear()
		}
	})

	acc.OnIdentify(func() {
		log.Println("acc.OnIdentify()")
	})

	t, err := hc.NewIPTransport(hc.Config{
		Pin: "32191123",
	}, acc.Accessory)
	if err != nil {
		log.Fatal(err)
	}

	hc.OnTermination(func() {
		<-t.Stop()
	})

	t.Start()
}
