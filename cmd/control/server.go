package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"shadowglass/internal/model"
	"sync"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"golang.org/x/sync/errgroup"
)

type server struct {
	db         *sql.DB
	natsServer *natsserver.Server
	natsConn   *nats.Conn
	nodesMutex sync.Mutex
	knownNodes []model.NodeRegistration
}

type goal struct {
	Containers []model.Container
}

func (s *server) run(ctx context.Context) error {
	slog.Info("CP: starting control")

	g := getGoal()

	wg, ctx := errgroup.WithContext(ctx)

	wg.Go(func() error {
		return s.handlePing()
	})

	wg.Go(func() error {
		return s.handleNodeRegistration()
	})

	wg.Go(func() error {
		return s.handleHeartbeat(ctx)
	})

	wg.Go(func() error {
		return s.issueWorkToNodes(ctx, g)
	})

	if err := wg.Wait(); err != nil {
		return fmt.Errorf("error group wait: %w", err)
	}

	return nil
}

func (s *server) handlePing() error {
	_, err := s.natsConn.Subscribe("agent.ping", func(msg *nats.Msg) {
		slog.Info("CP: received nats message", "msg", string(msg.Data))
	})
	if err != nil {
		return fmt.Errorf("subscribing to agent.ping: %w", err)
	}

	return nil
}

func (s *server) handleNodeRegistration() error {
	_, err := s.natsConn.Subscribe("agent.registration", func(msg *nats.Msg) {
		slog.Info("CP: received agent registration", "msg", string(msg.Data))

		var reg model.NodeRegistration
		err := json.NewDecoder(bytes.NewReader(msg.Data)).Decode(&reg)
		if err != nil {
			panic(err)
		}

		gbFree := float64(reg.FreeMemory) / 1024 / 1024 / 1024
		gbTotal := float64(reg.TotalMemory) / 1024 / 1024 / 1024

		s.nodesMutex.Lock()
		defer s.nodesMutex.Unlock()
		s.knownNodes = append(s.knownNodes, reg)

		slog.Info("CP: Known nodes", "knownNodes", s.knownNodes)

		slog.Info("CP: Decoded agent registration", "reg", reg, "gbFree", gbFree, "gbTotal", gbTotal)
	})
	if err != nil {
		return fmt.Errorf("subscribing to agent.registration: %w", err)
	}

	return nil
}

func (s *server) handleHeartbeat(ctx context.Context) error {
	for {
		slog.Info("CP: running")

		select {
		case <-time.After(time.Minute):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *server) issueCreateContainerRequest(ctx context.Context, hostname string, container model.Container) error {
	for {
		b, err := json.Marshal(container)
		if err != nil {
			return fmt.Errorf("marshalling container: %w", err)
		}

		err = s.natsConn.Publish(fmt.Sprintf("agent.action.%s", hostname), b)
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

func (s *server) issueWorkToNodes(ctx context.Context, goal goal) error {
	if len(s.knownNodes) > 0 {
		slog.Info("No known nodes. Will retry later")
	}

	for i, g := range goal.Containers {
		ni := i % len(s.knownNodes)
		node := s.knownNodes[ni]
		err := s.issueCreateContainerRequest(ctx, node.Hostname, g)
		if err != nil {
			return err
		}
	}

	return nil
}

func getGoal() goal {
	return goal{
		Containers: []model.Container{
			{
				Name:  "my-alpine-hello-world",
				Image: "alpine:latest",
				Env:   []string{"NAME=alpine"},
				Cmd:   []string{"echo", "Hello from ${NAME}!", "&&", "sleep", "infinity"},
				Count: 1,
			},
			{
				Name:  "my-debian-hello-world",
				Image: "debian:latest",
				Env:   []string{"NAME=debian"},
				Cmd:   []string{"echo", "Hello from ${NAME}!", "&&", "sleep", "infinity"},
				Count: 1,
			},
			{
				Name:  "my-ubuntu-hello-world",
				Image: "ubuntu:latest",
				Env:   []string{"NAME=ubuntu"},
				Cmd:   []string{"echo", "Hello from ${NAME}!", "&&", "sleep", "infinity"},
				Count: 1,
			},
		},
	}
}
