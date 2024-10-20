package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"shadowglass/internal/model"
	"shadowglass/internal/state"
	"sync"

	"github.com/nats-io/nats.go"
)

type goal struct {
	goalMutex  sync.Mutex
	Containers map[string]model.Container
}

// Processor provides dependencies necessary to process incoming messages
type Processor struct {
	natsConnection *nats.Conn
	msgs           <-chan interface{}
	state          state.State
}

func newProcessor(conn *nats.Conn, msgs <-chan interface{}) *Processor {
	return &Processor{
		natsConnection: conn,
		msgs:           msgs,
		state:          state.New(),
	}
}

// Process receives messages, updates state, and issues any commands necessary
func (p *Processor) Process() error {
	for msg := range p.msgs {
		switch msg := msg.(type) {
		case model.Node:
			p.state.UpdateFromNode(msg)
		case model.Container:
			err := p.state.ConfirmContainer(msg.ContainerID, msg.Assignment.AgentID)
			if err != nil {
				slog.Error("unable to confirm goal assignment during container create message", "err", err)
			}

			slog.Info("received message confirming container creation", "agentID", msg.Assignment.AgentID, "containerID", msg.ContainerID)
		default:
			slog.Warn("unknown message type received")
		}

		next, err := p.state.GenerateNextAction()
		if err != nil {
			slog.Error("unable to generate required actions", "err", err)
		}

		if next.Action == model.ContainerActionCreate {
			var b bytes.Buffer
			err = json.NewEncoder(&b).Encode(next.Container)
			if err != nil {
				return err
			}

			err = p.natsConnection.Publish(fmt.Sprintf("agent.action.%s", next.Container.Assignment.AgentID), b.Bytes())
			if err != nil {
				return err
			}
		}
	}

	return nil
}
