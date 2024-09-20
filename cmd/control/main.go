package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"shadowglass/internal/model"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"golang.org/x/sync/errgroup"
)

const (
	exitCodeErr       = 1
	exitCodeInterrupt = 2
)

func main() {
	slog.Info("CP: starting control")

	opts := &server.Options{}
	ns, err := server.NewServer(opts)
	if err != nil {
		panic(err)
	}

	go ns.Start()

	ns.ConfigureLogger()

	// Reset any signals that were set by nats server Start
	signal.Reset()

	if !ns.ReadyForConnections(4 * time.Second) {
		panic("not ready for connection")
	}

	slog.Info("CP: NATS server is ready to accept connections")

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
		case <-signalChan: // first signal, cancel context
			slog.Info("CP: received cancellation. shutting down nats server")
			ns.Shutdown()
			cancel()
		case <-ctx.Done():
		}
		<-signalChan // second signal, hard exit
		os.Exit(exitCodeInterrupt)
	}()

	if err := run(ctx, ns); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(exitCodeErr)
	}
}

func run(ctx context.Context, ns *server.Server) error {
	wg, ctx := errgroup.WithContext(ctx)

	con, err := nats.Connect(ns.ClientURL(), nats.InProcessServer(ns))
	if err != nil {
		return fmt.Errorf("connecting to inprocess NATS server: %w", err)
	}
	defer con.Close()

	wg.Go(func() error {
		http.Handle("/updates/", http.StripPrefix("/updates/", http.FileServer(http.Dir("public"))))
		if err := http.ListenAndServe(":8080", nil); err != nil {
			return fmt.Errorf("http listen and serve: %w", err)
		}

		return nil
	})

	wg.Go(func() error {
		_, err = con.Subscribe("agent.ping", func(msg *nats.Msg) {
			slog.Info("CP: received nats message", "msg", string(msg.Data))
			// err := msg.Respond([]byte("pong"))
			// if err != nil {
			// 	slog.Info("CP: received error when trying to respond to ping", "err", err)
			// }
		})
		if err != nil {
			return fmt.Errorf("subscribing to agent.ping: %w", err)
		}

		return nil
	})

	wg.Go(func() error {
		for {
			b, err := json.Marshal(model.Container{
				Name:  "my-hello-world",
				Image: "hello-world:latest",
				Count: 1,
			})
			if err != nil {
				panic(err)
			}

			err = con.Publish("container.create", b)
			if err != nil {
				return fmt.Errorf("publish to container.create: %w", err)
			}

			select {
			case <-time.After(10 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})

	wg.Go(func() error {
		for {
			slog.Info("CP: running")

			select {
			case <-time.After(time.Minute):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})

	if err := wg.Wait(); err != nil {
		return fmt.Errorf("error group wait: %w", err)
	}

	return nil
}
