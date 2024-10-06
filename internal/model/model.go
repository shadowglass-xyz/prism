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
	Name       string            `json:"name"`
	Image      string            `json:"image"`
	Count      int               `json:"count"`
	Env        []string          `json:"env"`
	Labels     map[string]string `json:"labels"`
	Entrypoint string            `json:"entrypoint"`
	Cmd        []string          `json:"cmd"`
}

// NodeRegistration is used to send initial statistics about a system upon agent boot
type NodeRegistration struct {
	Hostname    string      `json:"hostname"`
	CPUs        int         `json:"cpus"`
	FreeMemory  uint64      `json:"freeMemory"`
	TotalMemory uint64      `json:"totalMemory"`
	Containers  []Container `json:"containers"`
}
