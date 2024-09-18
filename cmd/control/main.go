package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"golang.org/x/sync/errgroup"
)

func main() {
	ctx := context.Background()
	wg, ctx := errgroup.WithContext(ctx)

	opts := &server.Options{}
	ns, err := server.NewServer(opts)
	if err != nil {
		panic(err)
	}

	go ns.Start()

	// os reset signals that nats started

	ns.Shutdown()

	if !ns.ReadyForConnections(4 * time.Second) {
		panic("not ready for connection")
	}

	wg.Go(func() error {
		http.Handle("/updates/", http.StripPrefix("/updates/", http.FileServer(http.Dir("public"))))
		if err := http.ListenAndServe(":8080", nil); err != nil {
			return err
		}

		return nil
	})

	wg.Go(func() error {
		for {
			slog.Info("Control plane running test again")
			select {
			case <-time.After(time.Minute):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})

	if err := wg.Wait(); err != nil {
		panic(err)
	}
}
