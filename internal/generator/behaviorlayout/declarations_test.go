package behaviorlayout_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
)

// declarativeFixture is a DB service whose two tables implement test.Votes,
// a declarative behavior (D103's step 5), beside a Workflow field each.
const declarativeFixture = "testdata/services/fixture-declarative-db"

// A table that implements a declarative behavior (D103's step 5) puts its
// declaration in the layout document, rules and all, once whatever the
// number of tables that compose it, so an adapter hands it to its core
// before it checks the layout's specs; a layout whose uses name none holds
// no declarations member, as every layout before it.
func TestTheLayoutHoldsEachDeclarativeBehaviorsDeclaration(t *testing.T) {
	file, err := os.ReadFile(filepath.FromSlash("../../../runtime/engine/testdata/rules/declarations/test.Votes.behavior.json"))
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New(naming.Default())
	if err := reg.RegisterBehavior(registry.BehaviorSpec{Extension: "test", Declaration: file}); err != nil {
		t.Fatal(err)
	}
	schema, err := loader.LoadService(filepath.FromSlash(declarativeFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	layout, err := behaviorlayout.Of(schema, reg)
	if err != nil {
		t.Fatal(err)
	}
	if layout == nil || len(layout.Types) != 2 {
		t.Fatalf("layout = %+v, want Proposal and Motion", layout)
	}
	if len(layout.Declarations) != 1 {
		t.Fatalf("declarations = %d, want test.Votes once", len(layout.Declarations))
	}
	var declared map[string]json.RawMessage
	if err := json.Unmarshal(layout.Declarations[0], &declared); err != nil {
		t.Fatal(err)
	}
	if string(declared["name"]) != `"test.Votes"` || len(declared["rules"]) == 0 || len(declared["tables"]) == 0 {
		t.Fatalf("the declaration lacks its name, rules or tables: %s", layout.Declarations[0])
	}
	var use *behaviorlayout.Use
	for i := range layout.Types["Proposal"].Uses {
		if layout.Types["Proposal"].Uses[i].Behavior == "test.Votes" {
			use = &layout.Types["Proposal"].Uses[i]
		}
	}
	if use == nil || use.Member != "testVotes" || len(use.Tables) != 1 || use.Tables[0].Table != "proposal_test_votes_votes" {
		t.Errorf("Proposal's test.Votes use = %+v", use)
	}

	// The core's behaviors alone: no member.
	plain, err := behaviorlayout.Of(load(t))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		t.Fatal(err)
	}
	if _, ok := members["declarations"]; ok {
		t.Errorf("a layout of the core's behaviors holds declarations: %s", data)
	}
}
