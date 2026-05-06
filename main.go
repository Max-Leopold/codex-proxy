package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"
)

const (
	listenHost  = "127.0.0.1"
	defaultPort = 6769
)

type config struct {
	port      int
	codexHome string
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "codex-proxy: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := parseFlags(args)
	if err != nil {
		return err
	}

	tokens := &TokenSource{codexHome: cfg.codexHome}
	codex := &CodexClient{tokens: tokens}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{}))
	server := &Server{codex: codex, log: logger}

	addr := fmt.Sprintf("%s:%d", listenHost, cfg.port)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           server.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	fmt.Fprintf(os.Stderr, "listening on http://%s\n", addr)
	return httpServer.ListenAndServe()
}

func parseFlags(args []string) (config, error) {
	fs := flag.NewFlagSet("codex-proxy", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	cfg := config{}
	fs.IntVar(&cfg.port, "port", defaultPort, "port to listen on")
	fs.StringVar(&cfg.codexHome, "codex-home", "", "Codex home directory; defaults to CODEX_HOME or ~/.codex")

	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: codex-proxy [options]\n\nOptions:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if fs.NArg() != 0 {
		return cfg, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if cfg.port < 0 || cfg.port > 65535 {
		return cfg, fmt.Errorf("invalid --port %d", cfg.port)
	}
	return cfg, nil
}
