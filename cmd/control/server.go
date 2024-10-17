package main

import (
	"context"
	"database/sql"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"golang.org/x/sync/errgroup"
)

type server struct {
	db         *sql.DB
	natsServer *natsserver.Server
	natsConn   *nats.Conn
}

func (s *server) run(ctx context.Context) error {
	msgs := make(chan interface{})
	defer close(msgs)

	monitor, err := newMonitor(s.natsConn, msgs)
	if err != nil {
		return err
	}
	defer monitor.Close()

	wg, ctx := errgroup.WithContext(ctx)
	wg.Go(func() error {
		processor := newProcessor(s.natsConn, msgs)
		return processor.Process()
	})

	return wg.Wait()
}
