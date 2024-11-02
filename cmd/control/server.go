package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"shadowglass/internal/model"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"golang.org/x/sync/errgroup"
)

type server struct {
	db         *sql.DB
	natsServer *natsserver.Server
	natsConn   *nats.Conn
	store      jetstream.KeyValue
}

func (s *server) run(ctx context.Context) error {
	msgs := make(chan interface{})
	defer close(msgs)

	err := s.populateInitialContainers(ctx)
	if err != nil {
		return err
	}

	monitor, err := newMonitor(s.natsConn, msgs)
	if err != nil {
		return err
	}
	defer monitor.Close()

	wg, ctx := errgroup.WithContext(ctx)
	wg.Go(func() error {
		processor := newProcessor(s.natsConn, msgs)
		return processor.Process(ctx)
	})

	err = wg.Wait()

	slog.Info("finished waiting")

	return err
}

// TODO: Where does the goal come from... this should be loaded from sqlite
var initialState = map[string]model.Container{
	"yb44xb1mflxexo41dknhufm7": {
		ContainerID: "yb44xb1mflxexo41dknhufm7",
		Name:        "my-alpine-hello-world",
		Image:       "alpine:latest",
		Env:         []string{"NAME=alpine"},
		Cmd:         []string{"/bin/ash", "-c", "echo Hello from ${NAME}! && tail -f /dev/null"},
	},
	"qc4bhwtbc9w6pad8vs598yon": {
		ContainerID: "qc4bhwtbc9w6pad8vs598yon",
		Name:        "my-debian-hello-world",
		Image:       "debian:latest",
		Env:         []string{"NAME=debian"},
		Cmd:         []string{"/bin/bash", "-c", "echo Hello from ${NAME}! && tail -f /dev/null"},
	},
	"lqysc1uu466zvl6d78fyi4h1": {
		ContainerID: "lqysc1uu466zvl6d78fyi4h1",
		Name:        "my-ubuntu-hello-world",
		Image:       "ubuntu:latest",
		Env:         []string{"NAME=ubuntu"},
		Cmd:         []string{"/bin/bash", "-c", "echo Hello from ${NAME}! && tail -f /dev/null"},
	},
}

func (s *server) populateInitialContainers(ctx context.Context) error {
	for cID, container := range initialState {
		var b bytes.Buffer
		err := json.NewEncoder(&b).Encode(container)
		if err != nil {
			return err
		}

		revision, err := s.store.Create(ctx, fmt.Sprintf("container.%s", cID), b.Bytes())
		switch {
		case errors.Is(err, jetstream.ErrKeyExists):
			slog.Info("key already existed", "key", fmt.Sprintf("container.%s", cID))
		case err != nil:
			return err
		default:
			slog.Info("Key created", "key", fmt.Sprintf("container.%s", cID), "revision", revision)
		}
	}

	return nil
}
