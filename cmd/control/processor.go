package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"math/rand"
	"shadowglass/internal/model"
	"slices"
	"sync"

	"github.com/nats-io/nats.go"
)

type goal struct {
	Containers []model.Container
}

// Processor provides dependencies necessary to process incoming messages
type Processor struct {
	natsConnection *nats.Conn
	msgs           <-chan interface{}
	nodesMutex     sync.Mutex
	nodes          map[string]model.NodeUpdate
	goal           goal
}

func newProcessor(conn *nats.Conn, msgs <-chan interface{}) *Processor {
	return &Processor{
		natsConnection: conn,
		msgs:           msgs,
		nodes:          make(map[string]model.NodeUpdate),
		goal:           getGoal(),
	}
}

// TODO: Where does the goal come from... this should be loaded from sqlite
func getGoal() goal {
	return goal{
		Containers: []model.Container{
			{
				Name:  "my-alpine-hello-world",
				Image: "alpine:latest",
				Env:   []string{"NAME=alpine"},
				Cmd:   []string{"/bin/ash", "-c", "echo Hello from ${NAME}! && tail -f /dev/null"},
			},
			{
				Name:  "my-debian-hello-world",
				Image: "debian:latest",
				Env:   []string{"NAME=debian"},
				Cmd:   []string{"/bin/bash", "-c", "echo Hello from ${NAME}! && tail -f /dev/null"},
			},
			{
				Name:  "my-ubuntu-hello-world",
				Image: "ubuntu:latest",
				Env:   []string{"NAME=ubuntu"},
				Cmd:   []string{"/bin/bash", "-c", "echo Hello from ${NAME}! && tail -f /dev/null"},
			},
		},
	}
}

// Process receives messages, updates state, and issues any commands necessary
func (p *Processor) Process() error {
	for msg := range p.msgs {
		switch msg := msg.(type) {
		case model.NodeUpdate:
			p.nodesMutex.Lock()
			p.nodes[msg.ID] = msg
			p.nodesMutex.Unlock()

			slog.Info("received message for node update", "msg", msg)
		default:
			slog.Warn("unknown message type received", "msg", msg)
		}

		// TODO: After each message reprocess the current state and determine if there are any changes necessary
		if err := p.DetermineStateChanges(); err != nil {
			return err
		}
	}

	return nil
}

// DetermineStateChanges looks for any missing or extra containers on the known nodes
func (p *Processor) DetermineStateChanges() error {
	p.nodesMutex.Lock()
	defer p.nodesMutex.Unlock()

	var nodeContainerNames []string
	for _, n := range p.nodes {
		for _, c := range n.Containers {
			nodeContainerNames = append(nodeContainerNames, c.Name)
		}
	}

	slog.Info("nodeContainerNames", "nodeContainerNames", nodeContainerNames)

	var goalContainerNames []string
	for _, g := range p.goal.Containers {
		goalContainerNames = append(goalContainerNames, g.Name)
	}

	slog.Info("goalContainerNames", "goalContainerNames", goalContainerNames)

	for _, c := range nodeContainerNames {
		if !slices.Contains(goalContainerNames, c) {
			slog.Info("detected extra container", "container", c)
			// TODO: Remove the extra container from the node. Remove it from the state so that assigning it to a node takes the removed container into account
		}
	}

	nodeIDs := slices.Collect(maps.Keys(p.nodes))
	for _, c := range p.goal.Containers {
		if !slices.Contains(nodeContainerNames, c.Name) {
			slog.Info("detected missing container", "container", c)

			assignmentNodeID := nodeIDs[rand.Intn(len(nodeIDs))]

			var b bytes.Buffer
			err := json.NewEncoder(&b).Encode(c)
			if err != nil {
				panic(err)
			}

			err = p.natsConnection.Publish(fmt.Sprintf("agent.action.%s", assignmentNodeID), b.Bytes())
			if err != nil {
				panic(err)
			}

			// If we have assigned a container to a node stop processing and wait for another message so we
			// might be able to get containers assigned to another node
			return nil

			// Send the message that we need to create this container on the node. And send it to "agent.action.agentID"

			// TODO: Determine which node to assign the missing container to based on:
			//   0. Basically useless (round robin assignments)
			//   1. container count to start (most basic)
			//   2. Resource usage/requested usage
		}
	}

	return nil
}

// func (p *Processor) issueWorkToNodes(ctx context.Context, goal goal) error {
// 	if len(p.nodes) > 0 {
// 		slog.Info("No known nodes. Will retry later")
// 	}
//
// 	for i, g := range goal.Containers {
// 		ni := i % len(s.nodes)
// 		node := s.nodes[ni]
// 		err := s.issueCreateContainerRequest(ctx, node.Hostname, g)
// 		if err != nil {
// 			return err
// 		}
// 	}
//
// 	return nil
// }
