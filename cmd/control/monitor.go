package main

import (
	"bytes"
	"encoding/json"
	"log/slog"

	"github.com/nats-io/nats.go"

	"github.com/shadowglass-xyz/prism/internal/model"
)

type monitor struct {
	natsConnection              *nats.Conn
	agentUpdateSubscription     *nats.Subscription
	containerCreateSubscription *nats.Subscription
	msgs                        chan<- interface{}
}

func newMonitor(conn *nats.Conn, msgs chan<- interface{}) (*monitor, error) {
	m := &monitor{
		natsConnection: conn,
		msgs:           msgs,
	}

	err := m.setupSubscriptions()
	if err != nil {
		return nil, err
	}

	return m, nil
}

func (m *monitor) setupSubscriptions() error {
	sub, err := m.natsConnection.Subscribe("agent.update.*", func(msg *nats.Msg) {
		var reg model.Node
		err := json.NewDecoder(bytes.NewReader(msg.Data)).Decode(&reg)
		if err != nil {
			panic(err)
		}

		m.msgs <- reg
	})
	if err != nil {
		return err
	}

	m.agentUpdateSubscription = sub

	sub, err = m.natsConnection.Subscribe("agent.container.create.>", func(msg *nats.Msg) {
		var cont model.Container
		err := json.NewDecoder(bytes.NewReader(msg.Data)).Decode(&cont)
		if err != nil {
			slog.Error("unable to decode message on subject", "subject", msg.Sub.Subject)
		}

		m.msgs <- cont
	})
	if err != nil {
		return err
	}

	m.containerCreateSubscription = sub

	return nil
}

func (m *monitor) Close() {
	_ = m.agentUpdateSubscription.Unsubscribe()
	_ = m.containerCreateSubscription.Unsubscribe()
}
