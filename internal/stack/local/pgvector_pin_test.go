package local_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/stack/local"
)

// TestVectorPostgresMatchesCIPin keeps the image the local target runs
// Postgres on for a stack with a vector column at PGVECTOR_VERSION and
// POSTGRES_VERSION in tools.env, the pgvector CI's vector tests run
// against (D84).
func TestVectorPostgresMatchesCIPin(t *testing.T) {
	tools, err := os.ReadFile(filepath.Join("..", "..", "..", "tools.env"))
	if err != nil {
		t.Fatal(err)
	}
	pgvector := regexp.MustCompile(`(?m)^PGVECTOR_VERSION=(\S+)$`).FindSubmatch(tools)
	postgres := regexp.MustCompile(`(?m)^POSTGRES_VERSION=(\S+)$`).FindSubmatch(tools)
	if pgvector == nil || postgres == nil {
		t.Fatal("tools.env pins no PGVECTOR_VERSION or POSTGRES_VERSION")
	}
	if want := "pgvector/pgvector:" + string(pgvector[1]) + "-pg" + string(postgres[1]); local.DefaultVectorPostgresImage != want {
		t.Errorf("the local target runs %s; tools.env pins CI's pgvector at %s", local.DefaultVectorPostgresImage, want)
	}
}

// TestPostgresImage: an environment's postgresImage wins; a stack with a
// vector column runs pgvector's image unless it names one, and any other
// the default (D84).
func TestPostgresImage(t *testing.T) {
	for _, tc := range []struct {
		env  registry.StackEnvironment
		want string
	}{
		{registry.StackEnvironment{}, local.DefaultPostgresImage},
		{registry.StackEnvironment{Vectors: true}, local.DefaultVectorPostgresImage},
		{registry.StackEnvironment{Vectors: true, Values: map[string]any{"postgresImage": "postgres:17-alpine"}}, "postgres:17-alpine"},
	} {
		if got := local.PostgresImage(tc.env); got != tc.want {
			t.Errorf("PostgresImage(%+v) = %s, want %s", tc.env, got, tc.want)
		}
	}
}
