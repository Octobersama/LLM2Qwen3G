package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"llm2qwen3guard/internal/config"
	"llm2qwen3guard/internal/server"
)

func main() {
	// -healthcheck performs a one-shot GET /healthz against the raw
	// LISTEN_ADDR env value (default ":8080", host part forced to 127.0.0.1)
	// and exits 0/1. Used by container health probes: the distroless runtime
	// image ships no shell/curl/wget. It reads LISTEN_ADDR directly and does
	// NOT run config.FromEnv, so it also works with missing upstream vars.
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

// runHealthcheck GETs /healthz on the address from the raw LISTEN_ADDR env
// value (default ":8080") and returns 0 only on HTTP 200. It intentionally
// does not require upstream credentials — it checks that the gateway process
// is serving.
func runHealthcheck() int {
	return probeHealthz(os.Getenv("LISTEN_ADDR"), &http.Client{Timeout: 3 * time.Second})
}

// probeHealthz resolves addr to a loopback health URL and performs one GET.
// addr forms: ":8080" (host -> 127.0.0.1), "127.0.0.1:8080", "[::1]:8080".
func probeHealthz(addr string, client *http.Client) int {
	url, err := healthzURL(addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
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

// healthzURL normalizes a listen address into http://host:port/healthz with
// the host defaulting to 127.0.0.1 (the gateway serves on all interfaces).
func healthzURL(addr string) (string, error) {
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("invalid LISTEN_ADDR %q: %w", addr, err)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz", nil
}
