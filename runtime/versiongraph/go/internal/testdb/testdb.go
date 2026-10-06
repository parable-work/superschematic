// Package testdb gives a version-graph test that holds or takes a graph's
// sweep lock a Postgres database of its own. Only tests import it.
//
// The sweep lock is an advisory lock, and Postgres keys an advisory lock to
// the database, not to a schema. In a database that tests share, one
// test's held lock makes another's sweep skip, and one test's sweep makes
// another's take of the lock fail. go test runs the packages' test
// binaries side by side, and tests in package engine and package postgres
// both hold and take the lock, so each of those tests runs in a database of
// its own, where no other test's lock reaches it.
package testdb

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

var counter atomic.Int64

// New creates a database on the server that dsn, a postgres:// URL whose
// role may create databases, names. It drops the database when the test
// ends and returns the database's URL.
func New(t testing.TB, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse the database URL: %v", err)
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = admin.Close(ctx) }()
	name := fmt.Sprintf("vg_test_%d_%d_%d", os.Getpid(), time.Now().UnixNano(), counter.Add(1))
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	t.Cleanup(func() {
		admin, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Errorf("connect to drop database %s: %v", name, err)
			return
		}
		defer func() { _ = admin.Close(ctx) }()
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})
	u.Path = "/" + name
	return u.String()
}
