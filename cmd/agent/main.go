package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/nats-io/nats.go"
	"golang.org/x/sync/errgroup"
)

const (
	defaultVersionCheckURL = "http://localhost:8080/updates/"
	version                = "0.3.4"
	exitCodeErr            = 1
	exitCodeInterrupt      = 2
)

func main() {
	slog.Info("AGENT: starting", "version", version)

	controlPlaneURL := os.Getenv("CONTROL_PLANE_URL")

	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt)
	defer func() {
		signal.Stop(signalChan)
		cancel()
	}()

	go func() {
		select {
		case <-signalChan:
			slog.Info("received first kill signal, cancel context")
			cancel()
		case <-ctx.Done():
		}
		slog.Info("received second kill signal, hard exit")
		<-signalChan
		os.Exit(exitCodeInterrupt)
	}()

	if err := run(ctx, controlPlaneURL); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(exitCodeErr)
	}
}

func run(ctx context.Context, controlPlaneURL string) error {
	wg, ctx := errgroup.WithContext(ctx)

	con, err := nats.Connect(controlPlaneURL)
	if err != nil {
		return fmt.Errorf("connecting to control plane: %w", err)
	}
	defer con.Close()

	wg.Go(func() error {
		slog.Info("AGENT: connected to control plane", "url", controlPlaneURL)

		for {
			slog.Info("AGENT: published to agent.ping")
			err := con.Publish("agent.ping", []byte("ping"))
			if err != nil {
				return fmt.Errorf("publishing to agent.ping: %w", err)
			}

			select {
			case <-time.After(10 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})

	wg.Go(func() error {
		_, err := con.Subscribe("container.create", func(msg *nats.Msg) {
			slog.Info("AGENT: creating container", "container", string(msg.Data))
		})
		if err != nil {
			return fmt.Errorf("subscribing to container.create: %w", err)
		}

		return nil
	})

	wg.Go(func() error {
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(1 * time.Minute):
				slog.Info("AGENT: running")
			}
		}
	})

	return wg.Wait()
}
