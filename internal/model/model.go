package model

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

// Container represents a request to create a container
type Container struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Image      string            `json:"image"`
	Env        []string          `json:"env"`
	Labels     map[string]string `json:"labels"`
	Entrypoint string            `json:"entrypoint"`
	Cmd        []string          `json:"cmd"`
	Status     string            `json:"status"`
}

// NodeUpdate is used to send statistics about a system
type NodeUpdate struct {
	ID          string      `json:"id"`
	Hostname    string      `json:"hostname"`
	CPUs        int         `json:"cpus"`
	FreeMemory  uint64      `json:"freeMemory"`
	TotalMemory uint64      `json:"totalMemory"`
	Containers  []Container `json:"containers"`
}
