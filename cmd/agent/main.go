package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"shadowglass/internal/model"
	"slices"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/nats-io/nats.go"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
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

	if err := run(ctx, controlPlaneURL); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(exitCodeErr)
	}
}

func run(ctx context.Context, controlPlaneURL string) error {
	wg, ctx := errgroup.WithContext(ctx)

	con, err := nats.Connect(controlPlaneURL)
	if err != nil {
		return fmt.Errorf("connecting to control plane: %w", err)
	}
	defer con.Close()

	wg.Go(func() error {
		slog.Info("AGENT: connected to control plane", "url", controlPlaneURL)

		for {
			slog.Info("AGENT: published to agent.ping")
			err := con.Publish("agent.ping", []byte("ping"))
			if err != nil {
				return fmt.Errorf("publishing to agent.ping: %w", err)
			}

			select {
			case <-time.After(10 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})

	wg.Go(func() error {
		_, err := con.Subscribe("container.create", func(msg *nats.Msg) {
			var container model.Container
			err := json.Unmarshal(msg.Data, &container)
			if err != nil {
				panic(err)
			}

			slog.Info("AGENT: creating container", "container", container)
			addContainerToState(container.Name, container)
		})
		if err != nil {
			return fmt.Errorf("subscribing to container.create: %w", err)
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

	wg.Go(func() error {
		return dockerReconcileLoop(ctx)
	})

	return wg.Wait()
}

func dockerReconcileLoop(ctx context.Context) error {
	for {
		cli, err := client.NewClientWithOpts(client.WithVersion("1.47"))
		if err != nil {
			return fmt.Errorf("connecting to docker: %w", err)
		}

		containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
		if err != nil {
			panic(err)
		}

		slog.Info("Received docker containers", "containers", containers)

		state := getState()
		slog.Info("current state", "state", state)

		for name, c := range state {
			foundContainer := false
			for _, rc := range containers {
				if slices.Contains(rc.Names, "/"+name) {
					foundContainer = true
					slog.Info("need to update a container")
					err := cli.ContainerStop(ctx, rc.ID, container.StopOptions{})
					if err != nil {
						slog.Warn("error stopping container", "container", rc, "err", err)
					}

					err = cli.ContainerRemove(ctx, rc.ID, container.RemoveOptions{})
					if err != nil {
						slog.Warn("error removing container", "container", rc, "err", err)
					}
				}
			}

			if !foundContainer {
				b, err := cli.ImagePull(ctx, c.Image, image.PullOptions{})
				if err != nil {
					panic(err)
				}
				defer b.Close()

				slog.Info("need to create a new container")
				// Container is completely missing so we have to create it
				resp, err := cli.ContainerCreate(ctx,
					&container.Config{
						Image: c.Image,
						Cmd:   c.Cmd,
					},
					&container.HostConfig{},
					&network.NetworkingConfig{},
					&v1.Platform{},
					name,
				)
				if err != nil {
					slog.Info("unable to create container", "err", err)
				}

				err = cli.ContainerStart(ctx, resp.ID, container.StartOptions{})
				if err != nil {
					slog.Info("unable to start container", "err", err)
				}
			}
		}

		select {
		case <-time.After(10 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
