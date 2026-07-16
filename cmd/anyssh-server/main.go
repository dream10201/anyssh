package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	appserver "anyssh/internal/server"
)

func main() {
	rotateDefault, err := durationEnv("ANYSSH_CLIENT_ROTATE", time.Hour)
	if err != nil {
		slog.Error("invalid environment", "name", "ANYSSH_CLIENT_ROTATE", "error", err)
		os.Exit(2)
	}
	listen := flag.String("listen", envOr("ANYSSH_LISTEN", ":8080"), "HTTP listen address")
	secret := flag.String("secret", envOr("ANYSSH_SECRET", ""), "optional shared secret required from clients")
	publicURL := flag.String("public-url", envOr("ANYSSH_PUBLIC_URL", ""), "public server URL used by the one-line installer")
	clientRotate := flag.Duration("client-rotate", rotateDefault, "access link rotation interval installed clients will use")
	dataFile := flag.String("data-file", envOr("ANYSSH_DATA_FILE", "anyssh-state.json"), "persistent admin settings file")
	flag.Parse()

	srv, err := appserver.New(appserver.Config{
		SharedSecret: *secret,
		PublicURL:    *publicURL,
		ClientRotate: *clientRotate,
		DataFile:     *dataFile,
	})
	if err != nil {
		slog.Error("initialize server", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	slog.Info("server listening", "address", *listen)
	if err := appserver.Serve(ctx, *listen, srv.Handler()); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func durationEnv(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	return time.ParseDuration(value)
}
