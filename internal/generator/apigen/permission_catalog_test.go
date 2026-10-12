package apigen_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/permcatalog"
)

// checkPermissionCatalog writes output's API module and compares the
// permissions.json beside its openapi.json with
// testdata/golden/<golden>/permissions.json, rewriting it under -update. It
// returns the catalog.
func checkPermissionCatalog(t *testing.T, output *apigen.APIOutput, golden string) permcatalog.Catalog {
	t.Helper()
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(outDir, permcatalog.FileName))
	if err != nil {
		t.Fatalf("read the written catalog: %v", err)
	}
	if string(got) != output.PermissionCatalogJSON {
		t.Errorf("the written %s is not the output's catalog", permcatalog.FileName)
	}
	goldenPath := filepath.Join("testdata", "golden", golden, permcatalog.FileName)
	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
	} else if want, err := os.ReadFile(goldenPath); err != nil {
		t.Fatalf("read golden: %v (run with -update to create)", err)
	} else if string(got) != string(want) {
		t.Errorf("%s differs from golden (run with -update to accept):\n%s", goldenPath, got)
	}
	catalog, err := permcatalog.Parse(got)
	if err != nil {
		t.Fatalf("the catalog does not parse: %v", err)
	}
	return catalog
}

// TestPermissionCatalogUserRoutesGolden pins the catalog of
// fixture-user-routes-api: the four administration permissions, each with
// the identity routes that need it, and nothing for the session routes or
// the project's greet, which name none. Regenerate with
// go test ./internal/generator/apigen -run TestPermissionCatalog -update
func TestPermissionCatalogUserRoutesGolden(t *testing.T) {
	catalog := checkPermissionCatalog(t, generateUserRoutesAPI(t, loadUserRoutesAPI(t)), userRoutesAPI)
	want := []string{"identity.roles.read", "identity.roles.write", "identity.users.read", "identity.users.write"}
	if !slices.Equal(catalog.Names(), want) {
		t.Fatalf("the catalog lists %v, want %v", catalog.Names(), want)
	}
	if catalog.API != userRoutesAPI || catalog.AuthDB != "fixture-user-model-db" {
		t.Errorf("the catalog is %s's with authDb %q", catalog.API, catalog.AuthDB)
	}
	for _, p := range catalog.Permissions {
		if !p.Identity {
			t.Errorf("%s is not marked as the identity routes' own", p.Name)
		}
	}
	if ops := catalog.Permissions[1].Operations; !slices.Equal(ops, []string{
		"AccountAdminCreateRoleHandler", "AccountAdminDeleteRoleHandler", "AccountAdminGrantRoleHandler",
		"AccountAdminRevokeRoleHandler", "AccountAdminUpdateRoleHandler",
	}) {
		t.Errorf("identity.roles.write is needed by %v", ops)
	}
}

// TestPermissionCatalogRequirePermissionGolden pins the catalog of
// fixture-api, whose operations name tenants.read and tenants.write with
// @requirePermission; none of them is the identity routes'. The module's
// own golden (TestWriteAPIGoldenSessionProvider) holds the same file.
func TestPermissionCatalogRequirePermissionGolden(t *testing.T) {
	catalog := checkPermissionCatalog(t, generateFixtureAPI(t), "fixture-api-session")
	if !slices.Equal(catalog.Names(), []string{"tenants.read", "tenants.write"}) {
		t.Fatalf("the catalog lists %v", catalog.Names())
	}
	for _, p := range catalog.Permissions {
		if p.Identity || len(p.Operations) == 0 {
			t.Errorf("%s: identity %v, operations %v", p.Name, p.Identity, p.Operations)
		}
	}
}

// TestNoPermissionCatalogWithoutPermissions: an API whose operations name
// no permission has no catalog, and its module has no permissions.json.
func TestNoPermissionCatalogWithoutPermissions(t *testing.T) {
	output := generateNestedArraysAPI(t)
	if output.PermissionCatalogJSON != "" {
		t.Fatalf("an API without permissions has the catalog %s", output.PermissionCatalogJSON)
	}
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outDir, permcatalog.FileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("WriteAPI wrote %s for an API without permissions: %v", permcatalog.FileName, err)
	}
}
