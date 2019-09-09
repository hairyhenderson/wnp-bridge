package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/color"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/lucasb-eyer/go-colorful"

	"github.com/pkg/errors"
)

type wifineopixel struct {
	address *url.URL
	hc      *http.Client
	state   []colorful.Color
	onState []colorful.Color
}

func newWifiNeopixel(addr string) (*wifineopixel, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}
	strip = &wifineopixel{
		address: u,
	}
	err = strip.initState()
	if err != nil {
		return nil, err
	}
	return strip, nil
}

func (w *wifineopixel) initState() (err error) {
	w.state, err = w.getStates()
	if err != nil {
		return err
	}
	if w.isOn() {
		w.onState = w.state
	}
	if w.isOff() {
		// init to red by default
		w.onState = make([]colorful.Color, len(w.state))
		for i := range w.state {
			w.onState[i] = colorful.LinearRgb(0xff, 0x00, 0x00)
		}
	}
	return nil
}

func (w *wifineopixel) numPixels() (int, error) {
	if w.hc == nil {
		w.hc = http.DefaultClient
	}
	resp, err := w.hc.Get(w.address.String() + "/size")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(body))
	n, err := strconv.ParseInt(s, 0, 32)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

func (w *wifineopixel) clear() error {
	if w.hc == nil {
		w.hc = http.DefaultClient
	}
	resp, err := w.hc.Get(w.address.String() + "/clear")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	log.Printf("clear: %v", string(body))
	w.state, err = w.getStates()
	if err != nil {
		return err
	}
	return nil
}

func (w *wifineopixel) on() error {
	if w.hc == nil {
		w.hc = http.DefaultClient
	}

	b := &bytes.Buffer{}
	err := json.NewEncoder(b).Encode(colorsToUint32(w.onState))
	if err != nil {
		return err
	}
	resp, err := w.hc.Post(w.address.String()+"/raw", "application/json", b)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	log.Printf("on: %v", string(body))
	return nil
}

func (w *wifineopixel) setState(state []colorful.Color) error {
	if w.hc == nil {
		w.hc = http.DefaultClient
	}
	if _, _, v := state[0].Hsv(); v > 0 {
		w.onState = w.state
	} else {
		w.onState = state
	}
	w.state = state

	b := &bytes.Buffer{}
	err := json.NewEncoder(b).Encode(colorsToUint32(state))
	if err != nil {
		return err
	}
	resp, err := w.hc.Post(w.address.String()+"/raw", "application/json", b)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	log.Printf("setState: %v", string(body))
	return nil
}

func (w *wifineopixel) setSolid(c colorful.Color) error {
	s := make([]colorful.Color, len(w.onState))
	for i := range s {
		s[i] = c
	}
	return w.setState(s)
}

func (w *wifineopixel) isOff() bool {
	for _, s := range w.state {
		r, g, b, _ := s.RGBA()
		if r != 0 || g != 0 || b != 0 {
			return false
		}
	}
	return true
}

func (w *wifineopixel) isOn() bool {
	for _, s := range w.state {
		r, g, b, _ := s.RGBA()
		if r != 0 || g != 0 || b != 0 {
			return true
		}
	}
	return false
}

func (w *wifineopixel) hsv() (h, s, v float64, err error) {
	c, err := strip.getState(0)
	if err != nil {
		return 0, 0, 0, err
	}
	h, s, v = c.Hsv()
	return h, s, v, nil
}

func (w *wifineopixel) getStates() ([]colorful.Color, error) {
	if w.hc == nil {
		w.hc = http.DefaultClient
	}
	resp, err := w.hc.Get(w.address.String() + "/states")
	if err != nil {
		return nil, err
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	states := []uint32{}
	err = json.Unmarshal(body, &states)
	if err != nil {
		return nil, err
	}

	c := make([]colorful.Color, len(states))
	for i, s := range states {
		c[i] = uint32ToColor(s)
	}

	return c, nil
}

func (w *wifineopixel) getState(pixel int) (state colorful.Color, err error) {
	w.state, err = w.getStates()
	if err != nil {
		return colorful.Color{}, err
	}

	return w.state[pixel], nil
}

func colorToUint32(c colorful.Color) uint32 {
	// A color's RGBA method returns values in the range [0, 65535]
	red, green, blue, alpha := c.RGBA()

	return (alpha>>8)<<24 | (red>>8)<<16 | (green>>8)<<8 | blue>>8
}

func colorsToUint32(c []colorful.Color) []uint32 {
	u := make([]uint32, len(c))
	for i := range c {
		u[i] = colorToUint32(c[i])
	}
	return u
}

func uint32ToColor(u uint32) colorful.Color {
	return colorful.Color{
		R: float64((uint8(u>>16) & 255) / 255),
		G: float64((uint8(u>>8) & 255) / 255),
		B: float64((uint8(u>>0) & 255) / 255),
	}
}

func parseHex(in string) (color.RGBA, error) {
	s := strings.TrimSpace(in)
	format := "%02x%02x%02x"
	var r, g, b uint8
	n, err := fmt.Sscanf(s, format, &r, &g, &b)
	if err != nil {
		return color.RGBA{}, err
	}
	if n != 3 {
		return color.RGBA{}, errors.Errorf("parseHex: %v is not a valid RGB Hex colour", s)
	}

	return color.RGBA{R: r, G: g, B: b, A: 0xff}, nil
}
