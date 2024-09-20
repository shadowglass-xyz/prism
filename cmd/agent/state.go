package main

import (
	"shadowglass/internal/model"
	"sync"
)

var state map[string]model.Container

func init() {
	state = make(map[string]model.Container)
}

var stateMutex sync.RWMutex

func addContainerToState(key string, container model.Container) {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	// It doesn't matter if this is a create or update since the full container must be passed in
	state[key] = container
}

func getState() map[string]model.Container {
	stateMutex.RLock()
	defer stateMutex.RUnlock()

	return state
}
