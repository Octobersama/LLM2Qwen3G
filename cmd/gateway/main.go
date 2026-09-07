package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"llm2qwen3guard/internal/config"
	"llm2qwen3guard/internal/server"
)

func main() {
	// -healthcheck performs a one-shot GET /healthz against the configured
	// listen address and exits 0/1. Used by container health probes: the
	// distroless runtime image ships no shell/curl/wget.
	healthcheck := flag.Bool("healthcheck", false, "probe GET /healthz on LISTEN_ADDR and exit")
	flag.Parse()
	if *healthcheck {
		os.Exit(runHealthcheck())
	}

	cfg, err := config.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	httpServer := &http.Server{Addr: cfg.ListenAddr, Handler: server.New(cfg)}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Printf("gateway listening on %s", cfg.ListenAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("gateway serve error: %v", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("gateway shutdown error: %v", err)
	}
}

// runHealthcheck GETs /healthz on the address from LISTEN_ADDR (default
// :8080) and returns 0 only on HTTP 200. It intentionally does not require
// upstream credentials — it checks that the gateway process is serving.
func runHealthcheck() int {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	if addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}
	url := "http://" + addr + "/healthz"
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: status %d\n", resp.StatusCode)
		return 1
	}
	return 0
}
