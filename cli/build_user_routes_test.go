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
// emits the filled sets (D50), each with the API key routes its apiKeys
// adds (D114).
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
	operations := map[string][]string{}
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			if op.IdentityOperation != "" {
				counts[set.Name]++
				operations[set.Name] = append(operations[set.Name], op.IdentityOperation)
			}
		}
	}
	assert.Equal(t, map[string]int{"Account": 9, "AccountAdmin": 15}, counts)
	assert.Equal(t, map[string][]string{
		"Account": {
			"login", "logout", "me", "capabilities", "changePassword", "register",
			"createApiKey", "listApiKeys", "revokeApiKey",
		},
		"AccountAdmin": {
			"createUser", "listUsers", "getUser", "disableUser", "enableUser", "approveUser", "setUserPassword",
			"listRoles", "createRole", "updateRole", "deleteRole", "grantRole", "revokeRole",
			"listUserApiKeys", "revokeUserApiKey",
		},
	}, operations)
}

// TestBuildCommand_WithDepsBuildsTheUserRoutes: build --with-deps reads the
// API's authDb from the build's own schema cache and writes the server
// without an implementation of the user model's operations, beside the
// OpenAPI document that describes their routes and the permission catalog
// that lists the permissions they need.
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
	for _, path := range []string{"/api/auth/keys", "/api/auth/keys/{id}", "/api/auth/admin/users/{id}/keys", "/api/auth/admin/users/{id}/keys/{keyId}"} {
		assert.Contains(t, string(openapi), `"`+path+`"`, "the OpenAPI document describes the API key route")
	}
	catalog, err := os.ReadFile(filepath.Join(apiDir, "permissions.json"))
	require.NoError(t, err)
	assert.Contains(t, string(catalog), `"authDb": "fixture-user-model-db"`)
	assert.Contains(t, string(catalog), `"name": "identity.users.write"`)
	assert.Contains(t, string(catalog), `"AccountAdminListUserApiKeysHandler"`)
	assert.Contains(t, string(catalog), `"AccountAdminRevokeUserApiKeyHandler"`)
	assert.NotContains(t, string(catalog), `"AccountCreateApiKeyHandler"`, "a user's own API key routes need no permission")
	interfaces, err := os.ReadFile(filepath.Join(apiDir, "interfaces.go"))
	require.NoError(t, err)
	assert.Contains(t, string(interfaces), "Greet(ctx context.Context)")
	assert.False(t, strings.Contains(string(interfaces), "Login("), "the implementation interfaces have a method for login")
	assert.False(t, strings.Contains(string(interfaces), "ApiKey"), "the implementation interfaces have a method for an API key route")
}
