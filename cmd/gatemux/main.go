package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/gatemux-dev/gatemux/internal/config"
	"github.com/gatemux-dev/gatemux/internal/server"
	"github.com/gatemux-dev/gatemux/internal/store"
)

// version is overridden at release build time via -ldflags "-X main.version=...".
var version = "0.0.0-dev"

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "version", "-v", "--version":
		fmt.Println(version)
	case "help", "-h", "--help":
		usage(os.Stdout)
	case "serve":
		if err := runServe(args); err != nil {
			fmt.Fprintf(os.Stderr, "serve: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		usage(os.Stderr)
		os.Exit(2)
	}
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", "examples/config.yaml", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	st, err := store.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	if err := store.Migrate(ctx, st.Pool); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	logger.Info("migrations applied")

	srv, err := server.New(cfg, st, logger)
	if err != nil {
		return fmt.Errorf("build server: %w", err)
	}
	// Also close workers and dependencies when listener startup fails.
	defer srv.Shutdown(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start() }()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("server: %w", err)
		}
		return nil
	}

	if err := srv.Shutdown(context.Background()); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	logger.Info("server stopped")
	return nil
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: gatemux <command> [flags]")
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  serve [--config PATH]   start the gateway server")
	fmt.Fprintln(w, "  version                 print version and exit")
	fmt.Fprintln(w, "  help                    show this help")
}
