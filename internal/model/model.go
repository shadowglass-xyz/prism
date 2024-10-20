package model

import "time"

type Action string

const (
	ContainerActionCreate Action = "create"
	ContainerActionUpdate Action = "update"
	ContainerActionDelete Action = "delete"
)

// ContainerAction communicates a desired change to the cluster
type ContainerAction struct {
	Action    Action
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

// Node is used to send statistics about a system
type Node struct {
	AgentID     string      `json:"agent_id"`
	Hostname    string      `json:"hostname"`
	CPUs        int         `json:"cpus"`
	FreeMemory  uint64      `json:"free_memory"`
	TotalMemory uint64      `json:"total_memory"`
	Containers  []Container `json:"containers"`
}
