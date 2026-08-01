// Command oidio is an OpenAI-compatible audio server (STT, and — as slices land —
// diarization, TTS, and realtime) backed by sherpa-onnx. Point any OpenAI client
// at it; corrallm proxies it like any other backend.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/iodesystems/oidio/internal/config"
	"github.com/iodesystems/oidio/internal/hf"
	"github.com/iodesystems/oidio/internal/server"
)

func main() {
	// Subcommands, with bare `oidio` still meaning `serve` — corrallm and every
	// existing unit file invoke it with flags and no verb.
	if len(os.Args) > 1 && os.Args[1] == "speakers" {
		if len(os.Args) > 2 && os.Args[2] == "review" {
			speakersReview(os.Args[3:])
			return
		}
		fmt.Fprintln(os.Stderr, "usage: oidio speakers review --audio FILE TRANSCRIPT.json")
		os.Exit(2)
	}
	if len(os.Args) > 1 && os.Args[1] == "verify" {
		if len(os.Args) > 2 && os.Args[2] == "render" {
			verifyRender(os.Args[3:])
			return
		}
		if len(os.Args) > 2 && os.Args[2] == "score" {
			verifyScore(os.Args[3:])
			return
		}
		verifyCmd(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}
	serve()
}

func serve() {
	cfgPath := flag.String("config", env("OIDIO_CONFIG", ""), "path to config file (default: ./oidio.yaml, then ~/.oidio/config.yml, then built-in defaults)")
	addr := flag.String("addr", env("OIDIO_ADDR", ""), "listen address (overrides config)")
	offline := flag.Bool("offline", env("OIDIO_OFFLINE", "") != "", "never fetch models; use only what is already in the hub cache")
	flag.Parse()

	// No config is a supported way to run: the built-in roster names hub
	// bundles, so a bare `oidio` serves all four surfaces on a machine where
	// nothing has been downloaded or written down.
	cfg, src, err := config.LoadDiscovered(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	log.Printf("oidio config: %s", src)

	// Resolve bundle-relative paths to real files BEFORE the engines open
	// anything — sherpa takes filesystem paths, so the indirection must be gone
	// by the time a model loads. A cold cache downloads here, which is why this
	// logs rather than doing it silently.
	fetch := hf.New()
	fetch.Offline = *offline
	fetch.Logf = log.Printf
	if err := cfg.Resolve(context.Background(), fetch); err != nil {
		log.Fatalf("models: %v", err)
	}
	if *addr != "" {
		cfg.Addr = *addr
	}
	if cfg.Addr == "" {
		cfg.Addr = ":8077"
	}

	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	log.Printf("oidio listening on %s (sherpa-onnx %s)", cfg.Addr, server.SherpaVersion())
	log.Fatal(srv.ListenAndServe())
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
