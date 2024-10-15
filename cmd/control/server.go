package main

import (
	"context"
	"database/sql"
	"log/slog"

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
	slog.Info("CP: starting control")

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

	// TODO: Something should be waited upon

	// wg.Go(func() error {
	// 	return s.handleMessages(ctx, msgs)
	// })
	//
	// g := getGoal()
	//
	// wg.Go(func() error {
	// 	return s.handleHeartbeat(ctx)
	// })
	//
	// wg.Go(func() error {
	// 	return s.issueWorkToNodes(ctx, g)
	// })
	//
	// if err := wg.Wait(); err != nil {
	// 	return fmt.Errorf("error group wait: %w", err)
	// }
	//
	// return nil
}

// func (s *server) issueCreateContainerRequest(ctx context.Context, hostname string, container model.Container) error {
// 	for {
// 		b, err := json.Marshal(container)
// 		if err != nil {
// 			return fmt.Errorf("marshalling container: %w", err)
// 		}
//
// 		err = s.natsConn.Publish(fmt.Sprintf("agent.action.%s", hostname), b)
// 		if err != nil {
// 			return fmt.Errorf("publish to container.create: %w", err)
// 		}
//
// 		select {
// 		case <-time.After(10 * time.Second):
// 		case <-ctx.Done():
// 			return ctx.Err()
// 		}
// 	}
// }
