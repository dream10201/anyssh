package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"anyssh/internal/bootstrap"
	appclient "anyssh/internal/client"
)

func main() {
	cfg, err := embeddedClientConfig()
	if err != nil {
		slog.Error("load embedded configuration", "error", err)
		os.Exit(2)
	}
	client, err := appclient.New(cfg)
	if err != nil {
		slog.Error("invalid embedded configuration", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := client.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("client stopped", "error", err)
		os.Exit(1)
	}
}

func embeddedClientConfig() (appclient.Config, error) {
	embedded, found, err := bootstrap.ReadExecutable()
	if err != nil {
		return appclient.Config{}, err
	}
	if !found {
		return appclient.Config{}, errors.New("this base client has no server parameters; install it from the server /install endpoint")
	}
	rotate, err := time.ParseDuration(embedded.Rotate)
	if err != nil || rotate <= 0 {
		return appclient.Config{}, errors.New("invalid embedded rotation interval")
	}
	if embedded.ServerURL == "" {
		return appclient.Config{}, errors.New("embedded server parameter is required")
	}
	return appclient.Config{
		ServerURL:   embedded.ServerURL,
		PublicURL:   embedded.ServerURL,
		RotateEvery: rotate,
		Secret:      embedded.Secret,
	}, nil
}
