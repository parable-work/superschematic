package buildcache

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/buildplan"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	ir "github.com/parable-work/superschematic/ir"
)

// referenceTree is a repository whose services reach each other through
// their configs: shop-api authenticates against shop-db and calls billing.
// stack, orders and other have no config edges.
type referenceTree struct {
	repo        string
	schemasRoot string
	services    []buildplan.Service
}

func newReferenceTree(t *testing.T) referenceTree {
	t.Helper()
	repo := t.TempDir()
	schemasRoot := filepath.Join(repo, "schemas")
	writeFile(t, filepath.Join(schemasRoot, "package.json"), "{}")
	service := func(name string, kind ir.SchemaKind, cfg schemaconfig.SchemaConfig) buildplan.Service {
		dir := filepath.Join(schemasRoot, "services", name)
		writeFile(t, filepath.Join(dir, "src", name+".schema.ts"), "export class A {}")
		writeFile(t, filepath.Join(dir, "src", "service.generated.ts"), `export const S = service({ name: "`+name+`", kind: "`+string(kind)+`" });`)
		cfg.Name, cfg.Kind = name, kind
		return buildplan.Service{Name: name, Dir: dir, Config: &cfg}
	}
	return referenceTree{
		repo:        repo,
		schemasRoot: schemasRoot,
		services: []buildplan.Service{
			service("shop-db", ir.SchemaKindDB, schemaconfig.SchemaConfig{}),
			service("billing", ir.SchemaKindAPI, schemaconfig.SchemaConfig{}),
			service("shop-api", ir.SchemaKindAPI, schemaconfig.SchemaConfig{
				AuthDB: "shop-db",
				Calls:  []schemaconfig.ServiceDependency{{Name: "billing", Kind: ir.SchemaKindAPI}},
			}),
			service("orders", ir.SchemaKindAPI, schemaconfig.SchemaConfig{}),
			service("stack", ir.SchemaKindGeneral, schemaconfig.SchemaConfig{}),
			service("other", ir.SchemaKindGeneral, schemaconfig.SchemaConfig{}),
		},
	}
}

func (tree referenceTree) hashes(t *testing.T) map[string]string {
	t.Helper()
	hashes, err := ComputeInputHashes(tree.services, tree.repo, naming.Naming{})
	require.NoError(t, err)
	return hashes
}

func (tree referenceTree) edit(t *testing.T, service, file string) {
	t.Helper()
	path := filepath.Join(tree.schemasRoot, "services", service, "src", file)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	writeFile(t, path, string(data)+"\n// edited\n")
}

func (tree referenceTree) sentinel(service string) string {
	return filepath.Join(tree.schemasRoot, "services", service, "src", "service.generated.ts")
}

// A schema that references a service in a decorator argument is keyed on
// that service's sources and on those of every service its config reaches,
// and on nothing else. Without the depfile the same edits leave it alone,
// so the reference is what invalidates it (D41).
func TestSchemaReferenceInvalidatesTheReferencingService(t *testing.T) {
	tree := newReferenceTree(t)
	unreferenced := tree.hashes(t)
	tree.edit(t, "shop-api", "shop-api.schema.ts")
	assert.Equal(t, unreferenced["stack"], tree.hashes(t)["stack"], "stack changed with shop-api before it referenced it")

	require.NoError(t, WriteSchemaReferences(tree.schemasRoot, "stack", []ir.ServiceRef{{Name: "shop-api", Kind: ir.SchemaKindAPI}}, nil))
	before := tree.hashes(t)
	assert.NotEqual(t, unreferenced["stack"], before["stack"], "the depfile is not in stack's key")
	for _, edit := range []struct{ service, file string }{
		{"shop-api", "shop-api.schema.ts"},
		{"shop-db", "shop-db.schema.ts"},
		{"billing", "billing.schema.ts"},
	} {
		tree.edit(t, edit.service, edit.file)
		after := tree.hashes(t)
		assert.NotEqual(t, before["stack"], after["stack"], "editing %s left stack's key as it was", edit.service)
		before = after
	}
	tree.edit(t, "other", "other.schema.ts")
	assert.Equal(t, before["stack"], tree.hashes(t)["stack"], "an unreferenced service changed stack's key")

	// A renamed service no longer answers to the name the depfile holds,
	// until stack's next build records the new one.
	storefront := filepath.Join(tree.schemasRoot, "services", "storefront")
	require.NoError(t, os.Rename(filepath.Join(tree.schemasRoot, "services", "shop-api"), storefront))
	for i, service := range tree.services {
		if service.Name == "shop-api" {
			renamed := *service.Config
			renamed.Name = "storefront"
			tree.services[i] = buildplan.Service{Name: "storefront", Dir: storefront, Config: &renamed}
		}
	}
	assert.NotEqual(t, before["stack"], tree.hashes(t)["stack"], "stack kept its key after shop-api was renamed")
}

// An identity names a service and reads only its sentinel: the key covers
// that file, so editing the service's schema leaves the referencing service
// alone, and a new name or kind in the sentinel does not. Two services that
// name each other through identities hash with no edge between them.
func TestIdentityReferenceKeysOnlyTheSentinel(t *testing.T) {
	tree := newReferenceTree(t)
	require.NoError(t, WriteSchemaReferences(tree.schemasRoot, "shop-api", nil, []string{tree.sentinel("orders")}))
	require.NoError(t, WriteSchemaReferences(tree.schemasRoot, "orders", nil, []string{tree.sentinel("shop-api")}))
	_, err := buildplan.TopologicalSort(tree.services)
	require.NoError(t, err, "services that name each other through identities form a cycle")

	before := tree.hashes(t)
	tree.edit(t, "orders", "orders.schema.ts")
	after := tree.hashes(t)
	assert.NotEqual(t, before["orders"], after["orders"])
	assert.Equal(t, before["shop-api"], after["shop-api"], "an identity made shop-api depend on orders' schema")

	writeFile(t, tree.sentinel("orders"), `export const S = service({ name: "orders-v2", kind: "API" });`)
	assert.NotEqual(t, after["shop-api"], tree.hashes(t)["shop-api"], "shop-api kept its key after orders' identity changed")
}

// Two services that reference each other hash without a cycle: each key
// covers both sources.
func TestMutualReferencesHashWithoutACycle(t *testing.T) {
	tree := newReferenceTree(t)
	require.NoError(t, WriteSchemaReferences(tree.schemasRoot, "stack", []ir.ServiceRef{{Name: "orders", Kind: ir.SchemaKindAPI}}, nil))
	require.NoError(t, WriteSchemaReferences(tree.schemasRoot, "orders", []ir.ServiceRef{{Name: "stack", Kind: ir.SchemaKindGeneral}}, nil))
	before := tree.hashes(t)
	tree.edit(t, "stack", "stack.schema.ts")
	after := tree.hashes(t)
	assert.NotEqual(t, before["stack"], after["stack"])
	assert.NotEqual(t, before["orders"], after["orders"])
}

// The depfile holds names and repo-relative sentinel paths, sorted, leaves
// out a sentinel outside the schemas root, and goes away when a schema no
// longer has either.
func TestWriteSchemaReferences(t *testing.T) {
	tree := newReferenceTree(t)
	outside := filepath.Join(t.TempDir(), "service.generated.ts")
	writeFile(t, outside, "export const S = 1;")
	refs := []ir.ServiceRef{{Name: "shop-db", Kind: ir.SchemaKindDB}, {Name: "billing", Kind: ir.SchemaKindAPI}}
	require.NoError(t, WriteSchemaReferences(tree.schemasRoot, "stack", refs, []string{tree.sentinel("orders"), outside}))

	got := ReadSchemaReferences(tree.repo, "stack")
	assert.Equal(t, []string{"billing", "shop-db"}, got.References)
	assert.Equal(t, []string{"schemas/services/orders/src/service.generated.ts"}, got.Identities)

	require.NoError(t, WriteSchemaReferences(tree.schemasRoot, "stack", nil, nil))
	assert.NoFileExists(t, schemaReferencesPath(tree.schemasRoot, "stack"))
	assert.Equal(t, SchemaReferences{}, ReadSchemaReferences(tree.repo, "stack"))
}
