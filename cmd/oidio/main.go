// Command oidio is an OpenAI-compatible audio server (STT, and — as slices land —
// diarization, TTS, and realtime) backed by sherpa-onnx. Point any OpenAI client
// at it; corrallm proxies it like any other backend.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/iodesystems/oidio/internal/config"
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
		verifyCmd(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}
	serve()
}

func serve() {
	cfgPath := flag.String("config", env("OIDIO_CONFIG", "oidio.yaml"), "path to config file")
	addr := flag.String("addr", env("OIDIO_ADDR", ""), "listen address (overrides config)")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
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
