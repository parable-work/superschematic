package cli

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// edgeTarget registers a target with the cloudflare target's values and no
// platform, under a name of its own, which init writes the cloudflare
// environment on in the cloudflare target's place: the root module does
// not link extensions/cloudflare (D64).
type edgeTarget struct{}

func (edgeTarget) Name() string { return "edge" }

func (edgeTarget) Register(r *registry.Registry) error {
	return r.RegisterTarget(registry.TargetSpec{
		Name:        "edge",
		Extension:   "edge",
		Provisioner: stacktest.Provisioner,
		Values: []byte(`{"type": "object", "required": ["accountId"], "additionalProperties": false, "properties": {
			"accountId": {"type": "string", "pattern": "^[0-9a-f]{32}$"},
			"workersSubdomain": {"type": "string"},
			"secretsStoreId": {"type": "string"}}}`),
	})
}

// withEdge links edgeTarget as the cloudflare target init writes.
func withEdge(t *testing.T) []registry.Extension {
	t.Helper()
	prev := cloudflareTarget
	cloudflareTarget = "edge"
	t.Cleanup(func() { cloudflareTarget = prev })
	return []registry.Extension{&stacktest.Extension{}, edgeTarget{}}
}

// testAccount is a Cloudflare account's id.
const testAccount = "0123456789abcdef0123456789abcdef"

// TestStackInitCloudflare: --cloudflare-account writes the cloud
// environment on the cloudflare target, its account the one value, with
// the target's types for tsc; at a terminal, init asks for it once gcp is
// declined or not linked, and again after an id that does not hold.
func TestStackInitCloudflare(t *testing.T) {
	exts := withEdge(t)
	project := prepareInitProject(t)
	out, err := runStackInitCmd(t, exts, "--name", "notes", "--cloudflare-account", testAccount, "--environment", "Preprod")
	require.NoError(t, err, out)
	dir := filepath.Join(project, "schemas", "services", "notes")
	schema := readFile(t, filepath.Join(dir, "src", "stack.schema.ts"))
	assert.Contains(t, schema, `@environment({ target: "edge", edge: { accountId: "`+testAccount+`" } })`+"\nexport abstract class Preprod {}\n")
	assert.Contains(t, schema, "// Cloudflare: superschematic stack bootstrap Preprod prepares the account once,\n")
	assert.Contains(t, readFile(t, filepath.Join(dir, "src", "edge-target.ts")), "      values: { accountId: string; workersSubdomain?: string; secretsStoreId?: string };\n")
	assert.Contains(t, out, "  Preprod on edge, account "+testAccount+"\n")
	assert.Contains(t, out, "  superschematic stack bootstrap Preprod")

	p := &scriptedPrompter{answers: []string{
		"y",   // Cloudflare
		"",    // Staging
		"abc", // refused
		testAccount,
		"n", // CI
		"",  // the derived list
	}}
	withTerminal(t, p, nil)
	out, err = runStackInitCmd(t, exts, "--force", "--name", "notes")
	require.NoError(t, err, out)
	assert.Equal(t, []string{
		"Add an environment on Cloudflare (the cloudflare target)? [y/N]: ",
		"Its environment name [Staging]: ",
		"Cloudflare account id: ",
		"Cloudflare account id: ",
		"Write a GitHub Actions workflow for the stack (outputs.ci)? [y/N]: ",
		"APIs to expose, comma-separated, or none [notes-api]: ",
	}, p.prompts)
	require.Len(t, p.said, 1)
	assert.Contains(t, p.said[0], `the cloudflare environment's account "abc"`)
	assert.Contains(t, readFile(t, filepath.Join(dir, "src", "stack.schema.ts")), `@environment({ target: "edge", edge: { accountId: "`+testAccount+`" } })`+"\nexport abstract class Staging {}\n")
}

// TestStackInitCloudflareRefuses: a gcp project beside an account, an id
// that does not hold, and a binary without the target.
func TestStackInitCloudflareRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		exts func(t *testing.T) []registry.Extension
		args []string
		want string
	}{
		{"both clouds", withEdge, []string{"--cloudflare-account", testAccount, "--gcp-project", "notes-staging", "--region", "us-east1"}, "stack init writes one cloud environment"},
		{"a bad id", withEdge, []string{"--cloudflare-account", "ABC"}, `the cloudflare environment's account "ABC"`},
		{"the target unlinked", func(*testing.T) []registry.Extension { return nil }, []string{"--cloudflare-account", testAccount}, "a cloudflare environment needs the cloudflare target, which this binary does not link"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exts := tc.exts(t)
			prepareInitProject(t)
			out, err := runStackInitCmd(t, exts, tc.args...)
			require.Error(t, err, out)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
