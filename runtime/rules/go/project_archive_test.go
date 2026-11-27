package rules

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// A project's archive (D103's step 5) links in the stock one's place: its
// rules crate, which the build's rulescore generator writes, exports the
// core's ABI over the core's behaviors and the ones its tables compose,
// and scripts/rules-archive.sh --crate stages it as
// libsuperschematic_rules.a in a directory CGO_LDFLAGS names before this
// package's own. With RULES_COMPOSED naming those behaviors, which
// internal/generator/rulescore's test sets when it runs this over such an
// archive, the core this binding runs declares each beside the core's.
func TestAProjectArchiveDeclaresItsComposedBehaviors(t *testing.T) {
	composed := os.Getenv("RULES_COMPOSED")
	if composed == "" {
		t.Skip("RULES_COMPOSED names no behavior: internal/generator/rulescore's test runs this over a project's archive")
	}
	out, err := DeclarationsJSON()
	if err != nil {
		t.Fatal(err)
	}
	var declarations []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out, &declarations); err != nil {
		t.Fatalf("declarations: %v\n%s", err, out)
	}
	var names []string
	for _, d := range declarations {
		names = append(names, d.Name)
	}
	if !slices.Contains(names, "Workflow") {
		t.Errorf("the archive declares %v, without the core's Workflow", names)
	}
	for _, name := range strings.Split(composed, ",") {
		if !slices.Contains(names, name) {
			t.Errorf("the archive declares %v, without %s", names, name)
		}
	}
}
