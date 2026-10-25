// Package behaviortypestest loads, for the types generators' tests, the
// fixture whose types compose behaviors (D58):
// internal/loader/testdata/services/fixture-behavior-types-json. Its types
// compose the core's behaviors, each narrowed by its config, and
// acme.Rating, an extension's behavior with no narrowing, which Registry
// declares as the acme example's extension does.
package behaviortypestest

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// Service is the name the tests generate the fixture's packages under.
const Service = "fixture-behavior-types"

// Rating is acme.Rating's declaration, as
// examples/acme-schematic/ext/rating.behavior.json declares it, less its
// operations: an extension named acme registers it.
const Rating = `{
  "name": "acme.Rating",
  "description": "Shoppers rate a catalog item from one star to the configured maximum.",
  "configSchema": {
    "type": "object",
    "required": ["maxStars"],
    "additionalProperties": false,
    "properties": {
      "maxStars": { "type": "integer", "minimum": 3, "maximum": 10, "description": "The highest rating a shopper can give." }
    }
  },
  "fields": [
    { "name": "ratingCount", "description": "How many ratings the item has." },
    { "name": "ratingAverage", "description": "The mean rating; 0 before the first." }
  ]
}`

// Registry is the core's registry with acme.Rating registered as the acme
// extension's.
func Registry(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New(naming.Default())
	if err := reg.RegisterBehavior(registry.BehaviorSpec{Extension: "acme", Declaration: json.RawMessage(Rating)}); err != nil {
		t.Fatal(err)
	}
	return reg
}

// Dir is the fixture's service directory.
func Dir(t *testing.T) string {
	t.Helper()
	return filepath.Join(testpaths.RepoRoot(t), "internal", "loader", "testdata", "services", "fixture-behavior-types-json")
}

// Schema loads the fixture with reg, which must declare acme.Rating.
func Schema(t *testing.T, reg *registry.Registry) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(Dir(t), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", Service, err)
	}
	return schema
}

// TicketBehaviors is what a read of a Ticket returns under behaviors, as
// the engine writes it: an entry for each behavior that declares a field,
// with a value for every field but Revisions' pendingProposals and the
// close deadline. Each language's test decodes it into TicketBehaviors and
// writes it back unchanged.
const TicketBehaviors = `{
  "Workflow": { "status": "in-progress", "enteredAt": 1790790700324 },
  "Comments": { "messages": 2, "lastMessageAt": 1790790800000, "threads": { "open": 1, "resolved": 0 }, "composing": ["ana"] },
  "Revisions": { "revision": 3 },
  "Dependencies": { "blocked": false },
  "Links": { "targets": {
    "project": { "schema": "projects", "id": "launch" },
    "reviewers": [ { "schema": "people", "id": "ana" }, { "schema": "people", "id": "bo" } ],
    "spec": { "schema": "documents", "id": "brief", "revision": 2, "latest": 3, "stale": true } } },
  "Timers": { "deadlines": { "respond": { "dueAt": 1790805100324, "fired": true, "firedAt": 1790805160000, "outcome": "moved" } } },
  "Approvals": { "gates": { "done": { "signedBy": ["ana"] } } },
  "Numbering": { "seq": 7, "label": "TCK-0007" },
  "acme.Rating": { "ratingCount": 4, "ratingAverage": 4.5 }
}`
