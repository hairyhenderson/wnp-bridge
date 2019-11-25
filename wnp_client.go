package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/lucasb-eyer/go-colorful"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"

	"github.com/opentracing-contrib/go-stdlib/nethttp"
	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
)

type wifineopixel struct {
	address *url.URL
	hc      *http.Client
	state   []colorful.Color
	onState []colorful.Color
}

func newWifiNeopixel(ctx context.Context, addr string) (*wifineopixel, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Transport: instrumentHTTPClient("wnp_client", &nethttp.Transport{}),
	}
	strip = &wifineopixel{
		address: u,
		hc:      client,
	}
	err = strip.initState(ctx)
	if err != nil {
		return nil, err
	}
	return strip, nil
}

func (w *wifineopixel) initState(ctx context.Context) (err error) {
	span, ctx := createSpan(ctx, "initState")
	defer span.Finish()

	w.state, err = w.getStates(ctx)
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

func (w *wifineopixel) get(ctx context.Context, path string) (*http.Response, error) {
	return w.do(ctx, "GET", path, "", nil)
}

func (w *wifineopixel) post(ctx context.Context, path, contentType string, body io.Reader) (*http.Response, error) {
	return w.do(ctx, "POST", path, contentType, body)
}

func (w *wifineopixel) do(ctx context.Context, method, path, contentType string, body io.Reader) (*http.Response, error) {
	tracer := opentracing.GlobalTracer()
	req, err := http.NewRequestWithContext(ctx, method, w.address.String()+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req, ht := nethttp.TraceRequest(tracer, req,
		nethttp.OperationName(req.Proto+" "+req.Method+" "+req.URL.String()))
	defer ht.Finish()
	res, err := w.hc.Do(req)
	span := ht.Span()
	if span != nil {
		tagsFromRequest(span, req)
		tagsFromResponse(span, res)
		if err != nil {
			ext.Error.Set(span, true)
		}
	}
	return res, err
}

func (w *wifineopixel) numPixels(ctx context.Context) (int, error) {
	span, ctx := createSpan(ctx, "numPixels")
	defer span.Finish()

	resp, err := w.get(ctx, "/size")
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

func (w *wifineopixel) clear(ctx context.Context) error {
	span, ctx := createSpan(ctx, "clear")
	defer span.Finish()

	resp, err := w.get(ctx, "/clear")
	if err != nil {
		resp.Body.Close()
		return err
	}
	body, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	log.Debug().Msgf("clear: %v", string(body))
	w.state, err = w.getStates(ctx)
	if err != nil {
		return err
	}
	return nil
}

func (w *wifineopixel) on(ctx context.Context) error {
	span, ctx := createSpan(ctx, "on")
	defer span.Finish()

	b := &bytes.Buffer{}
	err := json.NewEncoder(b).Encode(colorsToUint32(w.onState))
	if err != nil {
		return err
	}
	span.SetTag("body", b)

	log.Debug().Str("body", b.String()).Msg("sending body")
	resp, err := w.post(ctx, "/raw", "application/json", b)
	if err != nil {
		resp.Body.Close()
		return err
	}
	body, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	log.Debug().Str("body", string(body)).Msg("on")
	w.state, err = w.getStates(ctx)
	if w.isOn() {
		w.onState = w.state
	}
	return err
}

func (w *wifineopixel) setState(ctx context.Context, state []colorful.Color) error {
	span, ctx := createSpan(ctx, "setState")
	defer span.Finish()
	span.SetTag("state", state)

	if _, _, v := state[0].Hsv(); v > 0 && w.isOn() {
		w.onState = w.state
	}
	w.state = state

	b := &bytes.Buffer{}
	err := json.NewEncoder(b).Encode(colorsToUint32(state))
	if err != nil {
		return err
	}
	span.SetTag("body", b)

	resp, err := w.post(ctx, "/raw", "application/json", b)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	log.Debug().Msgf("setState: %v", string(body))
	return nil
}

func (w *wifineopixel) setSolid(ctx context.Context, c colorful.Color) error {
	span, ctx := createSpan(ctx, "setSolid")
	defer span.Finish()

	log.Debug().Msgf("setSolid(%v)", c)
	s := make([]colorful.Color, len(w.onState))
	for i := range s {
		s[i] = c
	}
	return w.setState(ctx, s)
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

func (w *wifineopixel) hsv(ctx context.Context) (h, s, v float64, err error) {
	span, ctx := createSpan(ctx, "hsv")
	defer span.Finish()

	c, err := strip.getState(ctx, 0)
	if err != nil {
		return 0, 0, 0, err
	}
	h, s, v = c.Hsv()
	return h, s, v, nil
}

func (w *wifineopixel) getStates(ctx context.Context) ([]colorful.Color, error) {
	span, ctx := createSpan(ctx, "getStates")
	defer span.Finish()

	resp, err := w.get(ctx, "/states")
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
	log.Debug().Msgf("GET /states = %v", states)

	c := make([]colorful.Color, len(states))
	for i, s := range states {
		c[i] = uint32ToColor(s)
	}
	log.Debug().Msgf("uint32ToColor(%v) = %v", states[0], c[0])

	return c, nil
}

func (w *wifineopixel) getState(ctx context.Context, pixel int) (state colorful.Color, err error) {
	span, ctx := createSpan(ctx, "getState")
	defer span.Finish()

	w.state, err = w.getStates(ctx)
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
	rgba := color.RGBA{
		uint8(u>>16) & 255,
		uint8(u>>8) & 255,
		uint8(u>>0) & 255,
		// force Alpha to full
		255,
		// uint8(u>>24) & 255,
	}
	// log.Debug().Msgf("uint32ToColor(%v) = rgba %v", u, rgba)
	c, _ := colorful.MakeColor(rgba)
	return c
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
