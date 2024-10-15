package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"shadowglass/internal/id"
	"shadowglass/internal/model"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/nats-io/nats.go"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pbnjay/memory"
	"golang.org/x/sync/errgroup"
)

const (
	defaultVersionCheckURL = "http://localhost:8080/updates/"
	version                = "0.3.4"
	exitCodeErr            = 1
	exitCodeInterrupt      = 2
	dockerVersion          = "1.47"
)

func main() {
	slog.Info("AGENT: starting", "version", version)

	controlPlaneURL := os.Getenv("CONTROL_PLANE_URL")
	agentID := os.Getenv("PRISM_AGENT_NODE_ID")
	if agentID == "" {
		agentID = id.Generate()
	}

	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt)
	defer func() {
		signal.Stop(signalChan)
		cancel()
	}()

	go func() {
		select {
		case <-signalChan:
			slog.Info("received first kill signal, cancel context")
			cancel()
		case <-ctx.Done():
		}
		slog.Info("received second kill signal, hard exit")
		<-signalChan
		os.Exit(exitCodeInterrupt)
	}()

	if err := run(ctx, agentID, controlPlaneURL); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(exitCodeErr)
	}
}

func run(ctx context.Context, agentID, controlPlaneURL string) error {
	wg, ctx := errgroup.WithContext(ctx)

	con, err := nats.Connect(controlPlaneURL)
	if err != nil {
		return fmt.Errorf("connecting to control plane: %w", err)
	}
	defer con.Close()

	cli, err := client.NewClientWithOpts(client.WithVersion("1.46"))
	if err != nil {
		return fmt.Errorf("connecting to docker: %w", err)
	}

	// TODO: retrieve the current state store from NATs. Probably by phoning the control plane and registering
	// st := state.New()

	wg.Go(func() error {
		slog.Info("AGENT: connected to control plane", "url", controlPlaneURL)

		for {
			stats, err := gatherSystemUpdateMessage(ctx, cli, agentID)
			if err != nil {
				return err
			}

			var statsB bytes.Buffer
			err = json.NewEncoder(&statsB).Encode(stats)
			if err != nil {
				return err
			}

			slog.Info(fmt.Sprintf("AGENT: updated to agent.update.%s", agentID), "update", statsB.String())
			err = con.Publish(fmt.Sprintf("agent.update.%s", agentID), statsB.Bytes())
			if err != nil {
				return fmt.Errorf("publishing to agent.update: %w", err)
			}

			select {
			case <-time.After(1 * time.Minute):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})

	wg.Go(func() error {
		_, err := con.Subscribe(fmt.Sprintf("agent.action.%s", agentID), func(msg *nats.Msg) {
			var cont model.Container
			err := json.Unmarshal(msg.Data, &cont)
			if err != nil {
				panic(err)
			}

			slog.Info("AGENT: creating container", "agentID", agentID, "container", cont)
			l := cont.Labels
			if l == nil {
				l = make(map[string]string)
			}
			l["controlled-by"] = "prism"
			l["assigned-agent-id"] = agentID

			body, err := cli.ImagePull(ctx, cont.Image, image.PullOptions{})
			if err != nil {
				panic(err)
			}
			defer body.Close()

			clResp, err := cli.ContainerList(ctx, container.ListOptions{
				Filters: filters.NewArgs(filters.KeyValuePair{Key: "name", Value: cont.Name}),
			})
			if err != nil {
				panic(err)
			}

			for _, c := range clResp {
				slog.Info("removing duplicate container", "containerNames", c.Names)
				err := cli.ContainerStop(ctx, c.ID, container.StopOptions{})
				if err != nil {
					panic(err)
				}

				err = cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{})
				if err != nil {
					panic(err)
				}
			}

			init := true
			ccResp, err := cli.ContainerCreate(ctx,
				&container.Config{
					Image:  cont.Image,
					Cmd:    cont.Cmd,
					Env:    cont.Env,
					Labels: l,
				},
				&container.HostConfig{
					Init: &init,
				},
				&network.NetworkingConfig{},
				&v1.Platform{},
				cont.Name,
			)
			if err != nil {
				slog.Info("unable to create container", "err", err)
			}

			err = cli.ContainerStart(ctx, ccResp.ID, container.StartOptions{})
			if err != nil {
				slog.Info("unable to start container", "err", err)
			}

			cont.ID = ccResp.ID

			var b bytes.Buffer
			err = json.NewEncoder(&b).Encode(cont)
			if err != nil {
				slog.Info("unable to encode container for response to controller", "err", err)
			}

			err = con.Publish(fmt.Sprintf("agent.container.create.%s.%s", agentID, cont.ID), b.Bytes())
			if err != nil {
				slog.Info("unable to publish container create message to NATS", "err", err)
			}
		})
		if err != nil {
			return fmt.Errorf("subscribing to agent.action.%s: %w", agentID, err)
		}

		return nil
	})

	wg.Go(func() error {
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(1 * time.Minute):
				slog.Info("AGENT: running")
			}
		}
	})

	// wg.Go(func() error {
	// 	return dockerReconcileLoop(ctx, cli, hostname, &st)
	// })

	return wg.Wait()
}

// func dockerReconcileLoop(ctx context.Context, cli *client.Client, hostname string, st *state.State) error {
// 	for {
// 		containers, err := cli.ContainerList(ctx, container.ListOptions{
// 			All:     true,
// 			Filters: filters.NewArgs(filters.KeyValuePair{Key: "label", Value: "controlled-by=prism"}),
// 		})
// 		if err != nil {
// 			panic(err)
// 		}
//
// 		existingContainerNames := make([]string, 0, len(containers))
// 		for _, c := range containers {
// 			existingContainerNames = append(existingContainerNames, c.Names...)
// 		}
//
// 		slog.Info("Existing container names", "names", existingContainerNames)
//
// 		// Loop over requested state to find containers that shouldn't exist
//
// 		var missingContainers []string
// 		var existingContainers []string
// 		for _, name := range st.Names() {
// 			if !slices.Contains(existingContainerNames, name) {
// 				missingContainers = append(missingContainers, name)
// 			} else {
// 				existingContainers = append(existingContainers, name)
// 			}
// 		}
//
// 		slog.Info("Missing containers", "names", missingContainers)
// 		slog.Info("Existing containers", "names", existingContainers)
//
// 		// Loop over existing state to find missing containers
//
// 		var extraContainers []string
// 		for _, name := range existingContainerNames {
// 			if !slices.Contains(st.Names(), name) {
// 				extraContainers = append(extraContainers, name)
// 			}
// 		}
//
// 		slog.Info("Extra containers", "names", extraContainers)
//
// 		for _, name := range missingContainers {
// 			c, ok := st.Get(name)
// 			if !ok {
// 				slog.Info("unable to find container %s in state")
// 			}
//
// 			b, err := cli.ImagePull(ctx, c.Image, image.PullOptions{})
// 			if err != nil {
// 				panic(err)
// 			}
// 			defer b.Close()
//
// 			slog.Info("need to create a new container")
// 			// Container is completely missing so we have to create it
// 			resp, err := cli.ContainerCreate(ctx,
// 				&container.Config{
// 					Image: c.Image,
// 					Cmd:   c.Cmd,
// 					Labels: map[string]string{
// 						"controlled-by": "prism",
// 						"assigned-host": hostname,
// 					},
// 				},
// 				&container.HostConfig{},
// 				&network.NetworkingConfig{},
// 				&v1.Platform{},
// 				name,
// 			)
// 			if err != nil {
// 				slog.Info("unable to create container", "err", err)
// 			}
//
// 			err = cli.ContainerStart(ctx, resp.ID, container.StartOptions{})
// 			if err != nil {
// 				slog.Info("unable to start container", "err", err)
// 			}
// 		}
//
// 		// for name, c := range state {
// 		// 	foundContainer := false
// 		// 	for _, rc := range containers {
// 		// 		if slices.Contains(rc.Names, name) {
// 		// 			foundContainer = true
// 		// 			slog.Info("need to update a container")
// 		// 			err := cli.ContainerStop(ctx, rc.ID, container.StopOptions{})
// 		// 			if err != nil {
// 		// 				slog.Warn("error stopping container", "container", rc, "err", err)
// 		// 			}
// 		//
// 		// 			err = cli.ContainerRemove(ctx, rc.ID, container.RemoveOptions{})
// 		// 			if err != nil {
// 		// 				slog.Warn("error removing container", "container", rc, "err", err)
// 		// 			}
// 		// 		}
// 		// 	}
// 		//
// 		// 	if !foundContainer {
// 		// 		b, err := cli.ImagePull(ctx, c.Image, image.PullOptions{})
// 		// 		if err != nil {
// 		// 			panic(err)
// 		// 		}
// 		// 		defer b.Close()
// 		//
// 		// 		slog.Info("need to create a new container")
// 		// 		// Container is completely missing so we have to create it
// 		// 		resp, err := cli.ContainerCreate(ctx,
// 		// 			&container.Config{
// 		// 				Image: c.Image,
// 		// 				Cmd:   c.Cmd,
// 		// 			},
// 		// 			&container.HostConfig{},
// 		// 			&network.NetworkingConfig{},
// 		// 			&v1.Platform{},
// 		// 			name,
// 		// 		)
// 		// 		if err != nil {
// 		// 			slog.Info("unable to create container", "err", err)
// 		// 		}
// 		//
// 		// 		err = cli.ContainerStart(ctx, resp.ID, container.StartOptions{})
// 		// 		if err != nil {
// 		// 			slog.Info("unable to start container", "err", err)
// 		// 		}
// 		// 	}
// 		// }
//
// 		select {
// 		case <-time.After(10 * time.Second):
// 		case <-ctx.Done():
// 			return ctx.Err()
// 		}
// 	}
// }

func gatherSystemUpdateMessage(ctx context.Context, cli *client.Client, agentID string) (model.NodeUpdate, error) {
	fm := memory.FreeMemory()
	tm := memory.TotalMemory()

	hostname, err := os.Hostname()
	if err != nil {
		return model.NodeUpdate{}, err
	}

	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.KeyValuePair{Key: "label", Value: "controlled-by=prism"},
			filters.KeyValuePair{Key: "label", Value: fmt.Sprintf("assigned-agent-id=%s", agentID)}, // interesting... this line is probably not necessary when running in production
		),
	})
	if err != nil {
		panic(err)
	}

	var mc []model.Container
	for _, c := range containers {
		container, err := cli.ContainerInspect(ctx, c.ID)
		if err != nil {
			panic(err)
		}

		mc = append(mc, model.Container{
			ID:     container.ID,
			Name:   strings.TrimPrefix(container.Name, "/"),
			Image:  container.Image,
			Env:    container.Config.Env,
			Labels: container.Config.Labels,
			Cmd:    container.Config.Cmd,
			Status: container.State.Status,
		})
	}

	return model.NodeUpdate{
		ID:          agentID,
		Hostname:    hostname,
		CPUs:        runtime.NumCPU(),
		FreeMemory:  fm,
		TotalMemory: tm,
		Containers:  mc,
	}, nil
}
