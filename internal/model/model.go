package model

import "time"

// NodeCommandAction specifies what type of action is being pushed to the node agent
type NodeCommandAction string

// Enum values for possible node command actions
const (
	Create NodeCommandAction = "create"
	Delete NodeCommandAction = "delete"
	Update NodeCommandAction = "update"
)

// NodeCommand contains changes that should be pushed to the agent internal state. I.E. Add a container, or remove a container
type NodeCommand struct {
	Action    NodeCommandAction
	Container Container
}

// ContainerAssignment wraps all the fields necessary to assign and reassign containers to agents
type ContainerAssignment struct {
	AgentID     string    `json:"agent_id"`
	AssignedAt  time.Time `json:"assigned_at"`
	ConfirmedAt time.Time `json:"confirmed_at"`
}

// Container represents a request to create a container
type Container struct {
	ContainerID string              `json:"id"`
	Name        string              `json:"name"`
	Image       string              `json:"image"`
	Env         []string            `json:"env"`
	Labels      map[string]string   `json:"labels"`
	Entrypoint  string              `json:"entrypoint"`
	Cmd         []string            `json:"cmd"`
	Status      string              `json:"status"`
	Assignment  ContainerAssignment `json:"assignment"`
}

// AgentUpdate is used to send statistics about a system
type AgentUpdate struct {
	AgentID     string      `json:"agent_id"`
	Hostname    string      `json:"hostname"`
	CPUs        int         `json:"cpus"`
	FreeMemory  uint64      `json:"free_memory"`
	TotalMemory uint64      `json:"total_memory"`
	Containers  []Container `json:"containers"`
}
