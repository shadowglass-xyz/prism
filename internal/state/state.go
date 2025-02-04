// Package state keeps a goroutine safe internal state of the containers that should be present on the actual nodes
package state

import (
	"fmt"
	"log/slog"
	"math/rand"
	"slices"
	"sync"
	"time"

	"github.com/shadowglass-xyz/prism/internal/model"
)

// State represents the internal representation of state. It needs to be goroutine safe
type State struct {
	nodesMutex   sync.Mutex
	nodeIDs      []string
	desiredMutex sync.RWMutex
	desired      map[string]model.Container
	currentMutex sync.RWMutex
	current      map[string]model.Container
}

// New creates a new representation of state
func New() State {
	return State{
		// desired: getDesired(), // TODO: temporary... this should probably be loaded from sqlite
		current: make(map[string]model.Container),
	}
}

// UpdateFromNode takes in a current node and updates the current state from it.
// It should add missing containers and recognize when a container has been removed from a node.
func (st *State) UpdateFromNode(n model.Node) {
	st.nodesMutex.Lock()
	st.nodeIDs = append(st.nodeIDs, n.AgentID)
	st.nodesMutex.Unlock()

	for _, c := range n.Containers {
		err := st.ConfirmContainer(c.ContainerID, n.AgentID)
		if err != nil {
			slog.Error("unable to confirm goal assignment during agent update", "err", err)
		}
	}

	slog.Info("received message for node update", "agentID", n.AgentID, "containersCount", len(n.Containers))
}

// UpdateContainer will add a container to the state. An error will be returned if the container name was already in use
func (st *State) UpdateContainer(c model.Container) {
	st.currentMutex.Lock()
	st.current[c.ContainerID] = c
	st.currentMutex.Unlock()
}

// AssignContainer updates the state to assign a container to a specific agentID
func (st *State) AssignContainer(containerID, agentID string) (model.Container, error) {
	if c, ok := st.current[containerID]; ok {
		c.Assignment = model.ContainerAssignment{
			AgentID:    agentID,
			AssignedAt: time.Now(),
		}

		st.currentMutex.Lock()
		st.current[containerID] = c
		st.currentMutex.Unlock()

		return c, nil
	}

	return model.Container{}, fmt.Errorf("unable to update container assignment; container id %s was not found", containerID)
}

// ConfirmContainer will update a container in the current state to specify the agent that the container is assigned
// to as well as the time it was confirmed
func (st *State) ConfirmContainer(containerID, agentID string) error {
	if c, ok := st.current[containerID]; ok {
		if c.Assignment.AgentID == "" || c.Assignment.AgentID != agentID {
			slog.Info("agentID didn't match confirmation. updating", "assignmentAgentID", c.Assignment.AgentID, "confirmationAgentID", agentID)
			// this is probably an error
		}

		c.Assignment.ConfirmedAt = time.Now()

		slog.Info("confirming assignment for goal container", "containerID", containerID, "assignedAgentID", agentID)

		if c.Assignment.AgentID == agentID {
			c.Assignment.ConfirmedAt = time.Now()
		} else {
			slog.Warn("received confirmation message from the wrong agent", "expectedID", c.Assignment.AgentID, "receivedID", agentID)
		}

		st.currentMutex.Lock()
		st.current[containerID] = c
		st.currentMutex.Unlock()

		return nil
	}

	return fmt.Errorf("unable to find container to confirm the goal assignment: container id %s", containerID)
}

// GenerateNextActions will take the desired state and the current state and figure out what actions need to be
// executed in order to match the states
func (st *State) GenerateNextActions() ([]model.ContainerAction, error) {
	var currentContainerIDs []string
	st.currentMutex.RLock()
	for _, c := range st.current {
		currentContainerIDs = append(currentContainerIDs, c.ContainerID)
	}
	st.currentMutex.RUnlock()

	slog.Info("current", "containerIDs", currentContainerIDs)

	var desiredContainerIDs []string
	st.desiredMutex.RLock()
	for _, d := range st.desired {
		desiredContainerIDs = append(desiredContainerIDs, d.ContainerID)
	}
	st.desiredMutex.RUnlock()

	slog.Info("desired", "containerIDs", desiredContainerIDs)

	var actions []model.ContainerAction
	for _, c := range currentContainerIDs {
		if !slices.Contains(desiredContainerIDs, c) {
			slog.Info("detected extra container", "name", c)
			// TODO: figure out container deletes
			actions = append(actions, model.ContainerAction{
				Action:    model.ContainerActionDelete,
				Container: st.current[c],
			})
		}
	}

	nodeIDs := st.nodeIDs

	for _, c := range st.desired {
		// If container exists then we need to check it's validity
		if current, ok := st.current[c.ContainerID]; ok {
			// Assigned, unconfirmed, and assigned less than 30 seconds ago. Skip
			if current.Assignment.AgentID != "" && !current.Assignment.ConfirmedAt.IsZero() && time.Since(current.Assignment.ConfirmedAt) < 45*time.Second {
				// slog.Info("container confirmation age", "containerID", current.ContainerID, "age", time.Since(current.Assignment.ConfirmedAt))
				continue
			}

			slog.Info("found stale unclaimed container... replacing...")

			c = current
		}

		// If the container didn't exist then we need to assign it and create it
		slog.Info("detected missing container", "name", c.Name)

		assignmentNodeID := nodeIDs[rand.Intn(len(nodeIDs))]

		c.Assignment.AgentID = assignmentNodeID
		c.Assignment.AssignedAt = time.Now()

		st.UpdateContainer(c)
		assignedContainer, err := st.AssignContainer(c.ContainerID, assignmentNodeID)
		if err != nil {
			return nil, err
		}

		// TODO: this should only generate a single action so that there is time for other things to happen in the system
		// between assignments. For example. If all the nodes are joining at one time then we want to try and distritube
		// the containers between the nodes

		actions = append(actions, model.ContainerAction{
			Action:    model.ContainerActionCreate,
			Container: assignedContainer,
		})

		// Send the message that we need to create this container on the node. And send it to "agent.action.agentID"

		// TODO: Determine which node to assign the missing container to based on:
		//   0. Basically useless (round robin assignments)
		//   1. container count to start (most basic)
		//   2. Resource usage/requested usage

		// _, err = p.goal.updateGoalAssignment(cID, assignmentNodeID)
		// if err != nil {
		// 	return err
		// }

		// If we have assigned a container to a node stop processing and wait for another message so we
		// might be able to get containers assigned to another node
		// return nil
	}

	return actions, nil
}
