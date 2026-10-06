package main

// This test is copied into the entrypoint module of a server some
// environment places on Cloud SQL, beside the cloudsql.go the build wrote,
// by TestCloudSQLEntrypointConnectsBothWays, which sets the derived
// variables of a Cloud SQL connection in the environment.

import (
	"context"
	"net"
	"testing"
	"time"

	"cloud.google.com/go/cloudsqlconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/http/go/stackconfig"
)

// TestCloudSQLConfig: the derived variables of a Cloud SQL connection, as a
// gcp environment sets them, become a pool that dials nothing until it is
// used, then dials the instance through the connector and logs in to the
// database as the IAM user, with no password and no TLS of its own.
func TestCloudSQLConfig(t *testing.T) {
	db, err := stackconfig.LoadDatabase("SHOP_DB_DATABASE")
	if err != nil {
		t.Fatal(err)
	}
	if db.CloudSQL == nil {
		t.Fatalf("SHOP_DB_DATABASE is %+v, not a Cloud SQL connection", db)
	}
	want := *db.CloudSQL
	dialed := make(chan string, 4)
	config, err := cloudSQLConfig(want, func(ctx context.Context, instance string, _ ...cloudsqlconn.DialOption) (net.Conn, error) {
		dialed <- instance
		client, server := net.Pipe()
		go fakePostgres(t, server, want)
		return client, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	conn := config.ConnConfig
	if conn.Host != want.Instance || conn.User != want.User || conn.Database != want.Database {
		t.Errorf("config logs in to %s as %s on %s, want %s as %s on %s", conn.Database, conn.User, conn.Host, want.Database, want.User, want.Instance)
	}
	if conn.Password != "" || conn.TLSConfig != nil || len(conn.Fallbacks) != 0 {
		t.Errorf("config has a password %q, TLS %v or fallbacks %v; the connector's connection needs none", conn.Password, conn.TLSConfig, conn.Fallbacks)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	select {
	case instance := <-dialed:
		t.Fatalf("the pool dialed %s before its first use", instance)
	default:
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping through the dialer: %v", err)
	}
	if instance := <-dialed; instance != want.Instance {
		t.Errorf("the pool dialed %s, want %s", instance, want.Instance)
	}
}

// fakePostgres answers one connection as Postgres answers a login it
// trusts: it checks that the first message is the startup message, not a
// TLS request, and names the IAM user and the database, then admits it
// with no password, answers each query empty, and stops at Terminate.
func fakePostgres(t *testing.T, conn net.Conn, want stackconfig.CloudSQL) {
	defer conn.Close()
	backend := pgproto3.NewBackend(conn, conn)
	msg, err := backend.ReceiveStartupMessage()
	if err != nil {
		t.Errorf("receive the startup message: %v", err)
		return
	}
	startup, ok := msg.(*pgproto3.StartupMessage)
	if !ok {
		t.Errorf("the pool sent %T first, not a startup message", msg)
		return
	}
	if user, database := startup.Parameters["user"], startup.Parameters["database"]; user != want.User || database != want.Database {
		t.Errorf("the pool logs in to %s as %s, want %s as %s", database, user, want.Database, want.User)
	}
	backend.Send(&pgproto3.AuthenticationOk{})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if err := backend.Flush(); err != nil {
		t.Errorf("admit the login: %v", err)
		return
	}
	for {
		msg, err := backend.Receive()
		if err != nil {
			return
		}
		switch msg.(type) {
		case *pgproto3.Query:
			backend.Send(&pgproto3.EmptyQueryResponse{})
			backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			if err := backend.Flush(); err != nil {
				return
			}
		case *pgproto3.Terminate:
			return
		}
	}
}
