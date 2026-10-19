package main

// This test is copied, with cloudsql_test.go, into the entrypoint module of
// a server on an engine database some environment places on Cloud SQL
// (D58), beside the cloudsql.go the build wrote, by
// TestTheEngineReachesCloudSQLThroughASocket.

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/cloudsqlconn"
	"github.com/jackc/pgx/v5"
	"github.com/parable-work/superschematic/runtime/http/go/stackconfig"
	"go.uber.org/zap"
)

// TestCloudSQLEngineSocket: the engine's URL names a socket in a directory
// of its own, the IAM user and the database, with no password and no TLS;
// a client that connects to it is carried over the connector's dial to the
// instance, which admits the IAM user's login; and release closes the
// socket and removes its directory.
func TestCloudSQLEngineSocket(t *testing.T) {
	want := stackconfig.CloudSQL{Instance: "acme-staging:us-east1:desk-db", Database: "desk_db", User: "desk-api@acme-staging.iam"}
	dialed := make(chan string, 4)
	url, release, err := cloudSQLEngineSocket(want, func(ctx context.Context, instance string, _ ...cloudsqlconn.DialOption) (net.Conn, error) {
		dialed <- instance
		client, server := net.Pipe()
		go fakePostgres(t, server, want)
		return client, nil
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"host='/", "port=5432", "user='desk-api@acme-staging.iam'", "dbname='desk_db'", "sslmode=disable"} {
		if !strings.Contains(url, part) {
			t.Errorf("the engine's URL %s lacks %s", url, part)
		}
	}
	if strings.Contains(url, "password") {
		t.Errorf("the engine's URL %s has a password", url)
	}
	select {
	case instance := <-dialed:
		t.Fatalf("the socket dialed %s before a client connected", instance)
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect to the engine's socket: %v", err)
	}
	if err := conn.Ping(ctx); err != nil {
		t.Fatalf("ping through the socket: %v", err)
	}
	_ = conn.Close(ctx)
	if instance := <-dialed; instance != want.Instance {
		t.Errorf("the socket dialed %s, want %s", instance, want.Instance)
	}
	dir := strings.TrimSuffix(strings.TrimPrefix(strings.Fields(url)[0], "host='"), "'")
	release()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the socket's directory %s outlives release: %v", dir, err)
	}
}
