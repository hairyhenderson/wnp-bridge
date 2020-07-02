package main

import (
	"encoding/json"
	"image/color"
	"net/http"
	"net/http/httputil"
	"os"
	"sync"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})
	states := []uint32{0, 0, 0, 0, 0, 0, 0, 0}
	slock := &sync.RWMutex{}
	http.HandleFunc("/clear", func(w http.ResponseWriter, r *http.Request) {
		log.Info().Msg("/clear")
		dump, _ := httputil.DumpRequest(r, false)
		log.Debug().Bytes("req", dump).Msg("/clear")
		w.WriteHeader(http.StatusOK)
	})
	http.HandleFunc("/raw", func(w http.ResponseWriter, r *http.Request) {
		log.Info().Msg("/raw")
		dump, _ := httputil.DumpRequest(r, false)
		log.Debug().Bytes("req", dump).Msg("/raw")

		d := json.NewDecoder(r.Body)
		defer r.Body.Close()
		slock.Lock()
		defer slock.Unlock()
		_ = d.Decode(&states)
		log.Debug().Uints32("states", states).Msgf("/raw color: %v", uint32ToColor(states[0]))
		w.WriteHeader(http.StatusOK)
	})
	http.HandleFunc("/size", func(w http.ResponseWriter, r *http.Request) {
		log.Info().Msg("/size")
		dump, _ := httputil.DumpRequest(r, false)
		log.Debug().Bytes("req", dump).Msg("/size")

		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("8"))
		w.WriteHeader(http.StatusOK)
	})
	http.HandleFunc("/states", func(w http.ResponseWriter, r *http.Request) {
		log.Info().Msg("/states")
		dump, _ := httputil.DumpRequest(r, false)
		log.Debug().Bytes("req", dump).Msg("/states")

		slock.RLock()
		defer slock.RUnlock()
		e := json.NewEncoder(w)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = e.Encode(states)
	})

	if err := http.ListenAndServe(":8888", nil); err != nil {
		log.Error().Err(err).Send()
	}
}

func uint32ToColor(u uint32) color.Color {
	rgba := color.RGBA{
		uint8(u>>16) & 255,
		uint8(u>>8) & 255,
		uint8(u>>0) & 255,
		255,
	}
	return rgba
}
