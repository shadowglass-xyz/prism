package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/rpc"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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

	"github.com/shadowglass-xyz/prism/internal/id"
	"github.com/shadowglass-xyz/prism/internal/model"
	irpc "github.com/shadowglass-xyz/prism/internal/rpc"
)

const (
	version           = "0.3.4"
	exitCodeErr       = 1
	exitCodeInterrupt = 2
	dockerVersion     = "1.47"
)

type State struct {
	mu                sync.RWMutex
	containers        map[string]struct{}
	claims            map[string]struct{}
	activeConnections atomic.Int32
}

func (st *State) Agent(_ irpc.AgentStateArgs, reply *irpc.AgentStateReply) error {
	st.mu.RLock()
	defer st.mu.RUnlock()
	*reply = irpc.AgentStateReply{
		Containers: st.containers,
		Claims:     st.claims,
	}
	return nil
}

func (st *State) RemoveClaim(args irpc.RemoveClaimArgs, reply *irpc.RemoveClaimReply) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.claims, args.Claim)
	*reply = irpc.RemoveClaimReply{
		Success:    true,
		Containers: st.containers,
		Claims:     st.claims,
	}
	return nil
}

func (st *State) AddClaim(args irpc.AddClaimArgs, reply *irpc.AddClaimReply) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.claims[args.Claim] = struct{}{}
	*reply = irpc.AddClaimReply{
		Success:    true,
		Containers: st.containers,
		Claims:     st.claims,
	}
	return nil
}

func main() {
	controlPlaneURL := os.Getenv("CONTROL_PLANE_URL")
	agentID := os.Getenv("PRISM_AGENT_NODE_ID")
	if agentID == "" {
		agentID = id.Generate()
	}

	// Create custom slog handler
	logger := slog.With(slog.String("agentID", agentID))
	logger.Info("starting", "version", version)

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
			logger.Info("received first kill signal, cancel context")
			cancel()
		case <-ctx.Done():
		}
		logger.Info("received second kill signal, hard exit")
		<-signalChan
		os.Exit(exitCodeInterrupt)
	}()

	if err := run(ctx, logger, agentID, controlPlaneURL); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(exitCodeErr)
	}
}

func run(ctx context.Context, logger *slog.Logger, agentID, controlPlaneURL string) error {
	state := State{
		containers:        make(map[string]struct{}),
		claims:            make(map[string]struct{}),
		activeConnections: atomic.Int32{},
	}

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

	foundContainers := make(chan model.Container, 1)

	wg, ctx := errgroup.WithContext(ctx)
	wg.Go(func() error {
		defer close(foundContainers)
		// It should pull all known containers from the controller every 15 seconds
		for {
			logger.Info("updating known containers")
			updateKnownContainers(ctx, logger, kvState, foundContainers)

			select {
			case <-time.After(15 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})

	foundClaims := make(chan model.ContainerAssignment, 1)

	wg.Go(func() error {
		defer close(foundClaims)
		// It should separately pull all known claims and refresh them every 15 seconds
		for {
			logger.Info("updating known claims")
			updateKnownClaims(ctx, logger, kvClaims, foundClaims)

			select {
			case <-time.After(15 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})

	wg.Go(func() error {
		// If there are any unclaimed containers that the agent can process then they
		//	should be claimed and their docker containers started
		return nil
	})

	wg.Go(func() error {
		// It should separately renew owned claims every 15 seconds
		return nil
	})

	wg.Go(func() error {
		// Publish health metrics to controller
		return nil
	})

	wg.Go(func() error {
		// If the tui connects then the claiming process will be put into manual mode
		server := rpc.NewServer()
		err := server.Register(&state)
		if err != nil {
			return fmt.Errorf("rpc register: %w", err)
		}
		l, err := net.Listen("tcp", ":11245")
		if err != nil {
			return fmt.Errorf("listen error: %w", err)
		}

		// Accept and serve connections
		for {
			conn, err := l.Accept()
			if err != nil {
				logger.Error("Accept error", slog.Any("err", err))
				continue
			}

			logger.Info("New connection", slog.String("addr", conn.RemoteAddr().String()))
			state.activeConnections.Add(1)

			// Handle the connection in a goroutine
			go func() {
				server.ServeConn(conn)
				logger.Info("Connection closed", slog.String("addr", conn.RemoteAddr().String()))
				state.activeConnections.Add(-1)
			}()
		}
	})

	//
	// If the tui requests that a container be claimed then it will claimed and the
	//
	//	renewal process will renew those claims every 15 seconds
	//
	// If the tui requests that a containers claim be dropped it will stop renewing
	//
	//	the claim and within 30 seconds the container will be available to be
	//	picked up by another agent
	//
	// Future state, polling should be unnecessary. All the kv buckets can be
	//
	//	subscribed to and notifications received as soon as a new container is
	//	available or a claim is dropped.
	//
	// Future state, the claim doesn't need to expire naturally the agent should
	//
	//	notify the controller during shutdown or claim delete so another agent
	//	can pick it up immediately

	return wg.Wait()
}

func updateKnownContainers(ctx context.Context, logger *slog.Logger, kvState jetstream.KeyValue, foundContainers chan<- model.Container) error {
	lister, err := kvState.ListKeys(ctx)
	if err != nil {
		return fmt.Errorf("unable to list container keys: %s", err)
	}

	for k := range lister.Keys() {
		logger.Info("found container key", "key", k)

		val, err := kvState.Get(ctx, k)
		if err != nil {
			logger.Error("unable to get container kv value", "err", err)
			return err
		}

		var m model.Container
		err = json.NewDecoder(bytes.NewReader(val.Value())).Decode(&m)
		if err != nil {
			logger.Error("decoding jetstream container kv value", "err", err)
			return err
		}

		// TODO: should this only emit container changes? What should happen when a container is removed
		foundContainers <- m
	}

	return nil
}

func updateKnownClaims(ctx context.Context, logger *slog.Logger, kvClaims jetstream.KeyValue, foundClaims chan<- model.ContainerAssignment) error {
	lister, err := kvClaims.ListKeys(ctx)
	if err != nil {
		return fmt.Errorf("unable to list claim keys: %s", err)
	}
	for k := range lister.Keys() {
		logger.Info("found claim key", "key", k)

		val, err := kvClaims.Get(ctx, k)
		if err != nil {
			logger.Error("unable to get claim kv value", "err", err)
			return err
		}

		var m model.ContainerAssignment
		err = json.NewDecoder(bytes.NewReader(val.Value())).Decode(&m)
		if err != nil {
			logger.Error("decoding jetstream claim kv value", "err", err)
			return err
		}

		// TODO: should this only emit container changes? What should happen when a container is removed
		foundClaims <- m
	}

	return nil
}

func run(ctx context.Context, logger *slog.Logger, agentID, controlPlaneURL string) error {
	wg, ctx := errgroup.WithContext(ctx)

	state := State{
		containers: make(map[string]struct{}),
		claims:     make(map[string]struct{}),
	}

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

	var activeConnections atomic.Int32
	wg.Go(func() error {
		server := rpc.NewServer()
		err := server.Register(&state)
		if err != nil {
			return fmt.Errorf("rpc register: %w", err)
		}
		l, err := net.Listen("tcp", ":11245")
		if err != nil {
			return fmt.Errorf("listen error: %w", err)
		}

		// Accept and serve connections
		for {
			conn, err := l.Accept()
			if err != nil {
				logger.Error("Accept error", slog.Any("err", err))
				continue
			}

			logger.Info("New connection", slog.String("addr", conn.RemoteAddr().String()))
			activeConnections.Add(1)

			// Handle the connection in a goroutine
			go func() {
				server.ServeConn(conn)
				logger.Info("Connection closed", slog.String("addr", conn.RemoteAddr().String()))
				activeConnections.Add(-1)
			}()
		}
	})

	// Search for any additional containers to claim
	wg.Go(func() error {
		for {
			logger.Debug("current state", slog.Any("state", &state))
			if activeConnections.Load() > 0 {
				logger.Info("agent is in manual mode due to an established rpc connection", slog.Int("connections", int(activeConnections.Load())))
			} else {
				for k := range lister.Keys() {
					logger.Info("found key", "key", k)

					val, err := kvState.Get(ctx, k)
					if err != nil {
						logger.Error("unable to get kv value", "err", err)
						return err
					}

					logger.Info("attempting to assign value to self if unassigned")

					var m model.Container
					err = json.NewDecoder(bytes.NewReader(val.Value())).Decode(&m)
					if err != nil {
						logger.Error("decoding jetstream kv value", "err", err)
						return err
					}

					containerClaimKey := fmt.Sprintf("container.%s", m.ContainerID)
					state.containers[containerClaimKey] = struct{}{}

					// Attempt to claim this container. If the create/claim fails then it was already claimed.
					var assignment model.ContainerAssignment
					assignment.AgentID = agentID
					assignment.AssignedAt = time.Now()
					assignment.ConfirmedAt = time.Now()
					var b bytes.Buffer
					err = json.NewEncoder(&b).Encode(assignment)
					if err != nil {
						logger.Error("unable to encode assignment value", "err", err)
						continue
					}

					_, err = kvClaims.Create(ctx, containerClaimKey, b.Bytes())
					if errors.Is(err, jetstream.ErrKeyExists) {
						// container has already been claimed so move onto the next one
						logger.Warn("container already claimed. moving on", "key", containerClaimKey)
						continue
					}

					logger.Info("container claimed", "key", containerClaimKey)

					state.claims[containerClaimKey] = struct{}{}
				}
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(15 * time.Second):
			}
		}
	})

	// Push still alive message to controller
	wg.Go(func() error {
		for {
			for claimedContainerKey := range state.claims {
				entry, err := kvClaims.Get(ctx, claimedContainerKey)
				if err != nil {
					logger.Error("touching claim key failed", "key", claimedContainerKey, "err", err)
				}

				var assignment model.ContainerAssignment
				err = json.NewDecoder(bytes.NewReader(entry.Value())).Decode(&assignment)
				if err != nil {
					logger.Error("decoding container assignment from claim", "err", err)
				}

				assignment.ConfirmedAt = time.Now()

				var b bytes.Buffer
				err = json.NewEncoder(&b).Encode(assignment)
				if err != nil {
					logger.Error("encoding updated confirmed assignment", "err", err)
				}

				_, err = kvClaims.Update(ctx, claimedContainerKey, b.Bytes(), entry.Revision())
				if err != nil {
					logger.Error("updating claim on container key", "err", err)
				}

				logger.Info("touched claim", "key", claimedContainerKey)
			}

			<-time.After(15 * time.Second)
		}
	})

	// Publish health metrics to controller
	wg.Go(func() error {
		logger.Info("connected to control plane", "url", controlPlaneURL)

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

			logger.Info(fmt.Sprintf("send update to agent.update.%s", agentID), "containers", len(stats.Containers))
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

	// Old way was to be told by the controller that we needed to run a container
	wg.Go(func() error {
		_, err := conn.Subscribe(fmt.Sprintf("agent.action.%s", agentID), func(msg *nats.Msg) {
			var cont model.Container
			err := json.Unmarshal(msg.Data, &cont)
			if err != nil {
				logger.Error("unable to unmarshal agent.action", "err", err)
			}

			logger.Info("received request to create container", "agentID", agentID, "container", cont.Name)
			l := cont.Labels
			if l == nil {
				l = make(map[string]string)
			}
			l["controlled-by"] = "prism"
			l["assigned-agent-id"] = agentID
			l["prism-container-id"] = cont.ContainerID

			body, err := cli.ImagePull(ctx, cont.Image, image.PullOptions{})
			if err != nil {
				logger.Error("unable to pull image", "err", err)
			}
			defer body.Close()

			clResp, err := cli.ContainerList(ctx, container.ListOptions{
				Filters: filters.NewArgs(filters.KeyValuePair{Key: "name", Value: cont.Name}),
			})
			if err != nil {
				logger.Error("unable to list containers", "err", err)
			}

			for _, c := range clResp {
				logger.Info("removing duplicate container", "containerNames", c.Names)
				err := cli.ContainerStop(ctx, c.ID, container.StopOptions{})
				if err != nil {
					logger.Error("unable to stop container", "err", err)
					return
				}

				err = cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{})
				if err != nil {
					logger.Error("unable to remove container", "err", err)
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
				logger.Info("unable to create container", "err", err)
				return
			}

			err = cli.ContainerStart(ctx, ccResp.ID, container.StartOptions{})
			if err != nil {
				logger.Info("unable to start container", "err", err)
				return
			}

			var b bytes.Buffer
			err = json.NewEncoder(&b).Encode(cont)
			if err != nil {
				logger.Info("unable to encode container for response to controller", "err", err)
				return
			}

			err = conn.Publish(fmt.Sprintf("agent.container.create.%s.%s", agentID, cont.ContainerID), b.Bytes())
			if err != nil {
				logger.Info("unable to publish container create message to NATS", "err", err)
			}
		})
		if err != nil {
			return fmt.Errorf("subscribing to agent.action.%s: %w", agentID, err)
		}

		return nil
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
