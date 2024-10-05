package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"shadowglass/internal/model"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"golang.org/x/sync/errgroup"
)

type control struct {
	db         *sql.DB
	natsServer *natsserver.Server
	natsConn   *nats.Conn
}

func (c *control) run(ctx context.Context) error {
	slog.Info("CP: starting control")

	wg, ctx := errgroup.WithContext(ctx)

	wg.Go(func() error {
		return c.handlePing()
	})

	wg.Go(func() error {
		return c.handleNodeRegistration()
	})

	wg.Go(func() error {
		return c.handleHeartbeat(ctx)
	})

	wg.Go(func() error {
		return c.issueCreateContainerRequest(ctx)
	})

	if err := wg.Wait(); err != nil {
		return fmt.Errorf("error group wait: %w", err)
	}

	return nil
}

func (c *control) handlePing() error {
	_, err := c.natsConn.Subscribe("agent.ping", func(msg *nats.Msg) {
		slog.Info("CP: received nats message", "msg", string(msg.Data))
	})
	if err != nil {
		return fmt.Errorf("subscribing to agent.ping: %w", err)
	}

	return nil
}

func (c *control) handleNodeRegistration() error {
	_, err := c.natsConn.Subscribe("agent.registration", func(msg *nats.Msg) {
		slog.Info("CP: received agent registration", "msg", string(msg.Data))

		var reg model.NodeRegistration
		err := json.NewDecoder(bytes.NewReader(msg.Data)).Decode(&reg)
		if err != nil {
			panic(err)
		}

		gbFree := float64(reg.FreeMemory) / 1024 / 1024 / 1024
		gbTotal := float64(reg.TotalMemory) / 1024 / 1024 / 1024

		slog.Info("CP: Decoded agent registration", "reg", reg, "gbFree", gbFree, "gbTotal", gbTotal)
	})
	if err != nil {
		return fmt.Errorf("subscribing to agent.registration: %w", err)
	}

	return nil
}

func (c *control) handleHeartbeat(ctx context.Context) error {
	for {
		slog.Info("CP: running")

		select {
		case <-time.After(time.Minute):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *control) issueCreateContainerRequest(ctx context.Context) error {
	for {
		b, err := json.Marshal(model.Container{
			Name:  "my-alpine-hello-world",
			Image: "alpine:latest",
			Cmd:   []string{"echo", "Hello from Docker!"},
			Count: 1,
		})
		if err != nil {
			return fmt.Errorf("marshalling container: %w", err)
		}

		err = c.natsConn.Publish("container.create", b)
		if err != nil {
			return fmt.Errorf("publish to container.create: %w", err)
		}

		select {
		case <-time.After(10 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
