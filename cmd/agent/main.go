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

	kvStore, err := jsConn.CreateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: "prism-state",
	})
	if err != nil {
		return fmt.Errorf("unable to create key/value store: %s", err)
	}

	lister, err := kvStore.ListKeys(ctx)
	if err != nil {
		return fmt.Errorf("unable to list keys: %s", err)
	}

	for k := range lister.Keys() {
		slog.Info("found key", "key", k)

		val, err := kvStore.Get(ctx, k)
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

		slog.Info("decoded value", "container", m, "agentID", m.Assignment.AgentID)

		if m.Assignment.AgentID == "" {
			slog.Info("container was unassigned. assigning to self")
			m.Assignment.AgentID = agentID

			var b bytes.Buffer
			err = json.NewEncoder(&b).Encode(&m)
			if err != nil {
				slog.Error("encoding assigned jetstream value", "err", err)
				return err
			}

			newRevision, err := kvStore.Update(ctx, k, b.Bytes(), val.Revision())
			if err != nil {
				slog.Error("updating key to record assignment", "key", k, "revision", val.Revision(), "newRevision", newRevision)
				return err
			}
		} else {
			slog.Info("container was already assigned", "agentID", m.Assignment.AgentID)
		}
	}

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
