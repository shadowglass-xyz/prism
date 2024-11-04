package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/nats-io/nats.go/jetstream"
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
	slog.Info("starting", "version", version)

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

	conn, err := nats.Connect(controlPlaneURL)
	if err != nil {
		return fmt.Errorf("connecting to control plane: %w", err)
	}
	defer conn.Close()

	cli, err := client.NewClientWithOpts(client.WithVersion("1.46"))
	if err != nil {
		return fmt.Errorf("connecting to docker: %w", err)
	}

	jsConn, err := jetstream.New(conn)
	if err != nil {
		return fmt.Errorf("unable to connect to jetstream: %s", err)
	}

	kvState, err := jsConn.KeyValue(ctx, "prism-state")
	if err != nil {
		return fmt.Errorf("unable to connect to key/value store prism-state: %s", err)
	}

	kvClaims, err := jsConn.KeyValue(ctx, "prism-claims")
	if err != nil {
		return fmt.Errorf("unable to connect to key/value store prism-claims: %s", err)
	}

	lister, err := kvState.ListKeys(ctx)
	if err != nil {
		return fmt.Errorf("unable to list keys: %s", err)
	}

	// There are two buckets one for the data and one for the claims
	// If the agent finds a container that it can manage then it should
	// attempt to create a record in the claims bucket. The claims bucket
	// has a ttl of 30 seconds (this controls how quickly containers can
	// be reassigned when an agent goes away).

	var claimedContainerKeys []string

	for k := range lister.Keys() {
		slog.Info("found key", "key", k)

		val, err := kvState.Get(ctx, k)
		if err != nil {
			slog.Error("unable to get kv value", "err", err)
			return err
		}

		slog.Info("attempting to assign value to self if unassigned")

		var m model.Container
		err = json.NewDecoder(bytes.NewReader(val.Value())).Decode(&m)
		if err != nil {
			slog.Error("decoding jetstream kv value", "err", err)
			return err
		}

		containerClaimKey := fmt.Sprintf("container.%s", m.ContainerID)

		// Attempt to claim this container. If the create/claim fails then it was already claimed.
		var assignment model.ContainerAssignment
		assignment.AgentID = agentID
		assignment.AssignedAt = time.Now()
		assignment.ConfirmedAt = time.Now()
		var b bytes.Buffer
		err = json.NewEncoder(&b).Encode(assignment)
		if err != nil {
			slog.Error("unable to encode assignment value", "err", err)
			continue
		}

		_, err = kvClaims.Create(ctx, containerClaimKey, b.Bytes())
		if errors.Is(err, jetstream.ErrKeyExists) {
			// container has already been claimed so move onto the next one
			slog.Warn("container already claimed. moving on", "key", containerClaimKey)
			continue
		}

		slog.Info("container claimed", "key", containerClaimKey)

		claimedContainerKeys = append(claimedContainerKeys, containerClaimKey)

		// slow down a bit to allow other agents to claim containers
		<-time.After(time.Second)
	}

	wg.Go(func() error {
		for {
			for _, claimedContainerKey := range claimedContainerKeys {
				entry, err := kvClaims.Get(ctx, claimedContainerKey)
				if err != nil {
					slog.Error("touching claim key failed", "key", claimedContainerKey, "err", err)
				}

				var assignment model.ContainerAssignment
				err = json.NewDecoder(bytes.NewReader(entry.Value())).Decode(&assignment)
				if err != nil {
					slog.Error("decoding container assignment from claim", "err", err)
				}

				assignment.ConfirmedAt = time.Now()

				var b bytes.Buffer
				err = json.NewEncoder(&b).Encode(assignment)
				if err != nil {
					slog.Error("encoding updated confirmed assignment", "err", err)
				}

				_, err = kvClaims.Update(ctx, claimedContainerKey, b.Bytes(), entry.Revision())
				if err != nil {
					slog.Error("updating claim on container key", "err", err)
				}

				slog.Info("touched claim", "key", claimedContainerKey)
			}

			<-time.After(15 * time.Second)
		}
	})

	wg.Go(func() error {
		slog.Info("connected to control plane", "url", controlPlaneURL)

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

			slog.Info(fmt.Sprintf("send update to agent.update.%s", agentID), "containers", len(stats.Containers))
			err = conn.Publish(fmt.Sprintf("agent.update.%s", agentID), statsB.Bytes())
			if err != nil {
				return fmt.Errorf("error publishing to agent.update: %w", err)
			}

			select {
			case <-time.After(30 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})

	wg.Go(func() error {
		_, err := conn.Subscribe(fmt.Sprintf("agent.action.%s", agentID), func(msg *nats.Msg) {
			var cont model.Container
			err := json.Unmarshal(msg.Data, &cont)
			if err != nil {
				slog.Error("unable to unmarshal agent.action", "err", err)
			}

			slog.Info("received request to create container", "agentID", agentID, "container", cont.Name)
			l := cont.Labels
			if l == nil {
				l = make(map[string]string)
			}
			l["controlled-by"] = "prism"
			l["assigned-agent-id"] = agentID
			l["prism-container-id"] = cont.ContainerID

			body, err := cli.ImagePull(ctx, cont.Image, image.PullOptions{})
			if err != nil {
				slog.Error("unable to pull image", "err", err)
			}
			defer body.Close()

			clResp, err := cli.ContainerList(ctx, container.ListOptions{
				Filters: filters.NewArgs(filters.KeyValuePair{Key: "name", Value: cont.Name}),
			})
			if err != nil {
				slog.Error("unable to list containers", "err", err)
			}

			for _, c := range clResp {
				slog.Info("removing duplicate container", "containerNames", c.Names)
				err := cli.ContainerStop(ctx, c.ID, container.StopOptions{})
				if err != nil {
					slog.Error("unable to stop container", "err", err)
					return
				}

				err = cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{})
				if err != nil {
					slog.Error("unable to remove container", "err", err)
					return
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
				"", // blank container name will be auto generated by docker
			)
			if err != nil {
				slog.Info("unable to create container", "err", err)
				return
			}

			err = cli.ContainerStart(ctx, ccResp.ID, container.StartOptions{})
			if err != nil {
				slog.Info("unable to start container", "err", err)
				return
			}

			var b bytes.Buffer
			err = json.NewEncoder(&b).Encode(cont)
			if err != nil {
				slog.Info("unable to encode container for response to controller", "err", err)
				return
			}

			err = conn.Publish(fmt.Sprintf("agent.container.create.%s.%s", agentID, cont.ContainerID), b.Bytes())
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

	return wg.Wait()
}

func gatherSystemUpdateMessage(ctx context.Context, cli *client.Client, agentID string) (model.Node, error) {
	fm := memory.FreeMemory()
	tm := memory.TotalMemory()

	hostname, err := os.Hostname()
	if err != nil {
		return model.Node{}, err
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
			ContainerID: container.Config.Labels["prism-container-id"],
			Name:        strings.TrimPrefix(container.Name, "/"),
			Image:       container.Image,
			Env:         container.Config.Env,
			Labels:      container.Config.Labels,
			Cmd:         container.Config.Cmd,
			Status:      container.State.Status,
		})
	}

	return model.Node{
		AgentID:     agentID,
		Hostname:    hostname,
		CPUs:        runtime.NumCPU(),
		FreeMemory:  fm,
		TotalMemory: tm,
		Containers:  mc,
	}, nil
}
