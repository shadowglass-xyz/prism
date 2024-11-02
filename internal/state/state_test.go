package state

import (
	"shadowglass/internal/id"
	"shadowglass/internal/model"
	"testing"
)

func TestState(t *testing.T) {
	t.Run("AddAllMissingContainers", func(t *testing.T) {
		agentID := id.Generate()
		s := New()

		s.UpdateFromNode(model.Node{
			AgentID:     agentID,
			Hostname:    "firstNode",
			CPUs:        1,
			FreeMemory:  512 * 1024 * 1024,
			TotalMemory: 512 * 1024 * 1024,
			Containers:  []model.Container{},
		})

		actions, err := s.GenerateNextActions()
		if err != nil {
			t.Fatalf("generate actions returned an error: %s", err)
		}

		if len(actions[0].Action) != 2 {
			t.Fatalf("expected two actions but received :%d", len(actions[0].Action))
		}

		if actions[0].Action != model.ContainerActionCreate {
			t.Fatalf("first action generated should have been a create action")
		}
	})

	t.Run("RemoveExtraContainer", func(t *testing.T) {
		agentID := id.Generate()
		s := New()
		s.desired = map[string]model.Container{}

		s.UpdateFromNode(model.Node{
			AgentID:     agentID,
			Hostname:    "firstNode",
			CPUs:        1,
			FreeMemory:  512 * 1024 * 1024,
			TotalMemory: 512 * 1024 * 1024,
			Containers: []model.Container{
				{
					ContainerID: id.Generate(),
					Name:        "extra-container",
				},
			},
		})

		_, err := s.GenerateNextActions()
		if err != nil {
			t.Fatalf("generate actions returned an error: %s", err)
		}

		// TODO: should generate a delete container action
	})

	t.Run("DeleteAndCreate", func(t *testing.T) {
		agentID := id.Generate()
		newContainerID := id.Generate()

		s := New()
		s.desired = map[string]model.Container{
			newContainerID: {
				ContainerID: newContainerID,
				Name:        "new-container",
			},
		}

		s.UpdateFromNode(model.Node{
			AgentID:     agentID,
			Hostname:    "firstNode",
			CPUs:        1,
			FreeMemory:  512 * 1024 * 1024,
			TotalMemory: 512 * 1024 * 1024,
			Containers: []model.Container{
				{
					ContainerID: id.Generate(),
					Name:        "extra-container",
				},
			},
		})

		_, err := s.GenerateNextActions()
		if err != nil {
			t.Fatalf("generate actions returned an error: %s", err)
		}

		// TODO: First action should be a delete

		_, err = s.GenerateNextActions()
		if err != nil {
			t.Fatalf("generate actions returned an error: %s", err)
		}

		// TODO: Second action should be a delete
	})

	// TODO: Update
}

func verifyActionTypes(t *testing.T, actions []model.ContainerAction, expectedCreate, expectedUpdate, expectedDelete int) {
	statuses := map[model.Action]int{
		model.ContainerActionUpdate: 0,
		model.ContainerActionCreate: 0,
		model.ContainerActionDelete: 0,
	}

	for _, a := range actions {
		statuses[a.Action]++
	}

	if statuses[model.ContainerActionCreate] != expectedCreate {
		t.Fatalf("expected %d create actions to be generated but got %d", expectedCreate, statuses[model.ContainerActionCreate])
	}

	if statuses[model.ContainerActionUpdate] != expectedUpdate {
		t.Fatalf("expected %d update actions to be generated but got %d", expectedUpdate, statuses[model.ContainerActionUpdate])
	}

	if statuses[model.ContainerActionDelete] != expectedDelete {
		t.Fatalf("expected %d delete actions to be generated but got %d", expectedDelete, statuses[model.ContainerActionDelete])
	}
}
