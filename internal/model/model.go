package model

// Container represents a request to create a container
type Container struct {
	Name  string   `json:"name"`
	Image string   `json:"image"`
	Count int      `json:"count"`
	Cmd   []string `json:"cmd"`
}

// NodeRegistration is used to send initial statistics about a system upon agent boot
type NodeRegistration struct {
	Hostname    string `json:"hostname"`
	CPUs        int    `json:"cpus"`
	FreeMemory  uint64 `json:"freeMemory"`
	TotalMemory uint64 `json:"totalMemory"`
}
