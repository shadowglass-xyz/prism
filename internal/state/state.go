// Package state keeps a goroutine safe internal state of the containers that should be present on the actual nodes
package state

import (
	"fmt"
	"maps"
	"shadowglass/internal/model"
	"sync"
)

// State represents the internal representation of state. It needs to be goroutine safe
type State struct {
	mutex sync.RWMutex
	state map[string]model.Container
}

// New creates a new representation of state
func New() State {
	s := make(map[string]model.Container)

	return State{
		state: s,
	}
}

// Get a container by name from the state
func (st *State) Get(name string) (model.Container, bool) {
	st.mutex.RLock()
	defer st.mutex.RUnlock()

	if c, ok := st.state[name]; ok {
		return c, true
	}

	return model.Container{}, false
}

// AddContainer will add a container to the state. An error will be returned if the container name was already in use
func (st *State) AddContainer(m model.Container) error {
	st.mutex.Lock()
	defer st.mutex.Unlock()

	if _, ok := st.state[m.Name]; !ok {
		st.state[m.Name] = m
		return nil
	}

	return fmt.Errorf("a container with this name already exists: %s", m.Name)
}

// UpdateContainer will replace a container with a new one. The reconciliation loop will still be responsible for diffing
// the container and finding how to apply those actual differences
func (st *State) UpdateContainer(m model.Container) error {
	st.mutex.Lock()
	defer st.mutex.Unlock()

	if _, ok := st.state[m.Name]; ok {
		st.state[m.Name] = m
		return nil
	}

	return fmt.Errorf("a container with this name didn't exist: %s", m.Name)
}

func (st *State) Names() []string {
	var names []string
	for name := range maps.Keys(st.state) {
		names = append(names, name)
	}

	return names
}
