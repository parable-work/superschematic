package writer

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestWorkflowPrincipalsRoundTrip (D104): a table's Workflow that names who
// makes its transitions (by), the permission that moves a caller past them
// (overridePermission) and a move to the state the row is in answered as
// no change (repeat) is written in every format with them, `status:
// Workflow<{ ...; by: ["askedOf"]; ...; overridePermission: ...; repeat:
// "unchanged" }>` in TypeScript and the config's JSON in the data forms,
// and reads back to the same IR.
func TestWorkflowPrincipalsRoundTrip(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-workflow-principals-db")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	schema.Description = ""
	want := normalizeIR(t, schema)
	contains := map[Format][]string{
		FormatTS: {
			`import { AutoGenerate, Workflow, key } from "@superschematic/db";`,
			`  status: Workflow<{ overridePermission: "questions.moderate"; repeat: "unchanged"; states: ["open", "answered", "withdrawn", "closed"]; transitions: [{ by: ["askedOf"]; from: "open"; to: "answered" }, { by: ["createdBy"]; from: "open"; to: "withdrawn" }, { by: ["askedOf", "createdBy"]; from: "answered"; permission: "questions.close"; to: "closed" }, { from: "answered"; to: "open" }] }>;`,
			`  review: Workflow<{ states: ["pending", "reviewed"]; transitions: [{ from: "pending"; to: "reviewed" }] }>;`,
		},
		FormatJSON: {`"overridePermission": "questions.moderate"`, `"repeat": "unchanged"`, `"by": [`},
		FormatYAML: {"overridePermission: questions.moderate", "repeat: unchanged", "by:\n"},
	}
	for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
		t.Run(string(format), func(t *testing.T) {
			source := writtenSource(t, schema, format)
			for _, want := range contains[format] {
				if !strings.Contains(source, want) {
					t.Errorf("written %s lacks %q:\n%s", format, want, source)
				}
			}
			for _, added := range []string{"statusEnteredAt", "reviewEnteredAt", "optimistic", "@behavior"} {
				if strings.Contains(source, added) {
					t.Errorf("written %s carries the loader's %s:\n%s", format, added, source)
				}
			}
			if got := normalizeIR(t, writeAndReload(t, schema, format)); got != want {
				t.Errorf("IR mismatch after the %s round trip\nwant:\n%s\ngot:\n%s", format, want, got)
			}
		})
	}
}
