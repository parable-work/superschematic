package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildCommand_FillsTheUserRouteSets: a build of an API with
// @userSessions and @userAdministration reads its authDb beside it and
// emits the filled sets (D50).
func TestBuildCommand_FillsTheUserRouteSets(t *testing.T) {
	buf := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(buf)
	root.SetArgs([]string{"build", filepath.Join(tsreaderTestdata, "fixture-user-routes-api"), "--emit-ir"})
	require.NoError(t, root.Execute())

	var schema struct {
		OperationSets []struct {
			Name       string `json:"name"`
			Operations []struct {
				IdentityOperation string `json:"identityOperation"`
			} `json:"operations"`
		} `json:"operationSets"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &schema))
	counts := map[string]int{}
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			if op.IdentityOperation != "" {
				counts[set.Name]++
			}
		}
	}
	assert.Equal(t, map[string]int{"Account": 6, "AccountAdmin": 12}, counts)
}

// TestBuildCommand_WithDepsBuildsTheUserRoutes: build --with-deps reads the
// API's authDb from the build's own schema cache and writes the server
// without an implementation of the user model's operations, beside the
// OpenAPI document that describes their routes.
func TestBuildCommand_WithDepsBuildsTheUserRoutes(t *testing.T) {
	servicesRoot := prepareTSServicesRoot(t, "fixture-user-model-db", "fixture-user-routes-api")
	outDir := t.TempDir()
	buf := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(buf)
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"build", "--with-deps", filepath.Join(servicesRoot, "fixture-user-routes-api"), "--out", outDir, "--skip-format"})
	require.NoError(t, root.Execute())

	out := buf.String()
	assert.Contains(t, out, "OK: fixture-user-model-db (built)")
	assert.Contains(t, out, "OK: fixture-user-routes-api (built)")
	apiDir := filepath.Join(outDir, "api", "fixture-user-routes-api")
	openapi, err := os.ReadFile(filepath.Join(apiDir, "openapi.json"))
	require.NoError(t, err)
	assert.Contains(t, string(openapi), `"/api/auth/login"`)
	interfaces, err := os.ReadFile(filepath.Join(apiDir, "interfaces.go"))
	require.NoError(t, err)
	assert.Contains(t, string(interfaces), "Greet(ctx context.Context)")
	assert.False(t, strings.Contains(string(interfaces), "Login("), "the implementation interfaces have a method for login")
}
