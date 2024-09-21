package model

type Container struct {
	Name  string   `json:"name"`
	Image string   `json:"image"`
	Count int      `json:"count"`
	Cmd   []string `json:"cmd"`
}
