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
	"time"

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
	nodesMutex     sync.Mutex
	nodes          map[string]model.AgentUpdate
	goal           goal
}

func newProcessor(conn *nats.Conn, msgs <-chan interface{}) *Processor {
	return &Processor{
		natsConnection: conn,
		msgs:           msgs,
		nodes:          make(map[string]model.AgentUpdate),
		goal:           getGoal(),
	}
}

// TODO: Where does the goal come from... this should be loaded from sqlite
func getGoal() goal {
	return goal{
		Containers: map[string]model.Container{
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
		},
	}
}

// Process receives messages, updates state, and issues any commands necessary
func (p *Processor) Process() error {
	for msg := range p.msgs {
		switch msg := msg.(type) {
		case model.AgentUpdate:
			p.nodesMutex.Lock()
			p.nodes[msg.AgentID] = msg
			p.nodesMutex.Unlock()

			for _, c := range msg.Containers {
				_, err := p.goal.confirmGoalAssignment(c.ContainerID, msg.AgentID)
				if err != nil {
					slog.Error("unable to confirm goal assignment during agent update", "err", err)
				}
			}

			slog.Info("received message for node update", "agentID", msg.AgentID, "containersCount", len(msg.Containers))
		case model.Container:
			confirmedContainer, err := p.goal.confirmGoalAssignment(msg.ContainerID, msg.Assignment.AgentID)
			if err != nil {
				slog.Error("unable to confirm goal assignment during container create message", "err", err)
			}

			// Temporarily assign the container manually to the node. It is temporary because the next time the node updates the controller
			// this container will be replaced with a very similar container.
			// TODO: this is interesting because it means that the confirmed at time will contantly be replaced. Initially it will be
			// set correctly here but when the node posts it's update it will be confirmed again. Maybe this is desirable

			p.nodesMutex.Lock()
			if n, ok := p.nodes[msg.Assignment.AgentID]; ok {
				n.Containers = append(n.Containers, confirmedContainer)
				p.nodes[msg.Assignment.AgentID] = n
			}
			p.nodesMutex.Unlock()

			slog.Info("received message confirming container creation", "agentID", msg.Assignment.AgentID, "containerID", msg.ContainerID)
		default:
			slog.Warn("unknown message type received")
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

	var nodeContainerIDs []string
	for _, n := range p.nodes {
		for _, c := range n.Containers {
			nodeContainerIDs = append(nodeContainerIDs, c.ContainerID)
		}
	}

	slog.Info("node", "containerIDs", nodeContainerIDs)

	var goalContainerIDs []string
	for _, g := range p.goal.Containers {
		goalContainerIDs = append(goalContainerIDs, g.ContainerID)
	}

	slog.Info("goal", "containerIDs", goalContainerIDs)

	for _, c := range nodeContainerIDs {
		if !slices.Contains(goalContainerIDs, c) {
			slog.Info("detected extra container", "name", c)
			// TODO: Remove the extra container from the node. Remove it from the state so that assigning it to a node takes the removed container into account
		}
	}

	nodeIDs := slices.Collect(maps.Keys(p.nodes))
	for cID, c := range p.goal.Containers {
		// Assigned, unconfirmed, and assigned less than 30 seconds ago. Skip
		if c.Assignment.AgentID != "" && c.Assignment.ConfirmedAt.IsZero() && time.Since(c.Assignment.AssignedAt) < 30*time.Second {
			continue
		}

		if !slices.Contains(nodeContainerIDs, c.ContainerID) {
			slog.Info("detected missing container", "name", c.Name)

			assignmentNodeID := nodeIDs[rand.Intn(len(nodeIDs))]

			assignedContainer, err := p.goal.updateGoalAssignment(c.ContainerID, assignmentNodeID)
			if err != nil {
				return err
			}

			var b bytes.Buffer
			err = json.NewEncoder(&b).Encode(assignedContainer)
			if err != nil {
				return err
			}

			// We have published a message asking an agent to start a missing container. This needs to be optimistially set in the goal
			// state that it was completed so any messages that come immediately after this one will think that this part of the goal
			// is completed. If the node doesn't confirm that the container was created within a specific time frame then it should
			// be marked as incomplete again

			err = p.natsConnection.Publish(fmt.Sprintf("agent.action.%s", assignmentNodeID), b.Bytes())
			if err != nil {
				return err
			}

			_, err = p.goal.updateGoalAssignment(cID, assignmentNodeID)
			if err != nil {
				return err
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

func (g *goal) updateGoalAssignment(containerID, assignedAgentID string) (model.Container, error) {
	g.goalMutex.Lock()
	defer g.goalMutex.Unlock()

	if c, ok := g.Containers[containerID]; ok {
		c.Assignment = model.ContainerAssignment{
			AgentID:    assignedAgentID,
			AssignedAt: time.Now(),
		}

		g.Containers[containerID] = c
		return c, nil
	}

	return model.Container{}, fmt.Errorf("unable to update container assignment; container id %s", containerID)
}

func (g *goal) confirmGoalAssignment(containerID, assignmentAgentID string) (model.Container, error) {
	g.goalMutex.Lock()
	defer g.goalMutex.Unlock()

	if c, ok := g.Containers[containerID]; ok {
		if c.Assignment.ConfirmedAt.IsZero() {
			slog.Info("confirming assignment for goal container", "containerID", containerID, "assignedAgentID", assignmentAgentID)

			if c.Assignment.AgentID == assignmentAgentID {
				c.Assignment.ConfirmedAt = time.Now()
			} else {
				slog.Warn("received confirmation message from the wrong agent", "expectedID", c.Assignment.AgentID, "receivedID", assignmentAgentID)
			}

			g.Containers[containerID] = c
			return c, nil
		}
	}

	return model.Container{}, fmt.Errorf("unable to find container to confirm the goal assignment: container id %s", containerID)
}
