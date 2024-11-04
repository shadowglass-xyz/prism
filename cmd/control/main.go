package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"shadowglass"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

const (
	exitCodeErr       = 1
	exitCodeInterrupt = 2
	userID            = "iug6920l2cbz7mf6sg60sb29wxhqaz0o"
)

var pragmas = `
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;
PRAGMA foreign_keys = ON;
`

func main() {
	db, err := setupDB()
	if err != nil {
		panic(err)
	}
	defer db.Close()

	natsServer, err := setupNATSServer()
	if err != nil {
		panic(err)
	}
	defer natsServer.Shutdown()

	slog.Info("NATS server is ready to accept connections")

	// Configure context for shutdown handling
	ctx, cancel := context.WithCancel(context.Background())
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt)
	defer func() {
		signal.Stop(signalChan)
		cancel()
	}()

	go func() {
		select {
		case <-signalChan: // first signal, cancel context
			natsServer.Shutdown()
			cancel()
		case <-ctx.Done():
		}
		<-signalChan // second signal, hard exit
		os.Exit(exitCodeInterrupt)
	}()

	conn, err := nats.Connect(natsServer.ClientURL(), nats.InProcessServer(natsServer))
	if err != nil {
		panic(fmt.Sprintf("connecting to inprocess NATS server: %s", err))
	}
	defer conn.Close()

	jsConn, err := jetstream.New(conn)
	if err != nil {
		panic(fmt.Sprintf("unable to connect to jetstream: %s", err))
	}

	kvStore, err := jsConn.CreateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: "prism-state",
	})
	if err != nil {
		panic(fmt.Sprintf("unable to create key/value state store: %s", err))
	}

	_, err = jsConn.CreateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: "prism-claims",
		TTL:    30 * time.Second,
	})
	if err != nil {
		panic(fmt.Sprintf("unable to create key/value claims store: %s", err))
	}

	c := server{
		db:         db,
		natsServer: natsServer,
		natsConn:   conn,
		store:      kvStore,
	}

	if err := c.run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		if errors.Is(err, context.Canceled) {
			os.Exit(0)
		}

		os.Exit(exitCodeErr)
	}
}

func setupDB() (*sql.DB, error) {
	err := os.MkdirAll("db", os.ModePerm)
	if err != nil {
		return nil, fmt.Errorf("creating db directory for database: %w", err)
	}

	// setup database
	db, err := sql.Open("sqlite", "db/control.db")
	if err != nil {
		return nil, fmt.Errorf("opening connection to control database: %w", err)
	}

	_, err = db.Exec(pragmas)
	if err != nil {
		return nil, fmt.Errorf("setting pragmas on database connection: %w", err)
	}

	goose.SetBaseFS(shadowglass.MigrationsFS)

	if err := goose.SetDialect("sqlite3"); err != nil {
		return nil, fmt.Errorf("configuring goose migrator for sqlite3 dialect: %w", err)
	}

	if err := goose.Up(db, "migrations"); err != nil {
		return nil, fmt.Errorf("running goose migrations: %w", err)
	}

	return db, nil
}

func setupNATSServer() (*natsserver.Server, error) {
	opts := &natsserver.Options{
		Debug:     false,
		Trace:     false,
		JetStream: true,
		StoreDir:  "./storage",
	}
	ns, err := natsserver.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("setting up nats server: %w", err)
	}

	go ns.Start()

	ns.ConfigureLogger()

	maxWait := 4 * time.Second
	if !ns.ReadyForConnections(10 * time.Second) {
		return nil, fmt.Errorf("nats server wasn't able to accept connections after %s", maxWait)
	}

	// Reset any signals that were set by nats server Start
	signal.Reset()

	return ns, nil
}
