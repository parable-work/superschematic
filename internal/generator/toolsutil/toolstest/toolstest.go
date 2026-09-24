// Package toolstest holds what the SDK generators' tests share to check
// that an extension's tool invocation policy changes the tool documents
// only at the policy's own key and values.
package toolstest

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	ir "github.com/parable-work/superschematic/ir"
)

// ReviewPolicy is a distribution's invocation policy: its own key, three
// values in an order that is not sorted, and a default that is not the
// first.
var ReviewPolicy = apigen.ToolInvocationPolicy{
	Extension: "vendor",
	Key:       "review",
	Values:    []string{"never", "on-write", "always"},
	Default:   "on-write",
}

// UseReviewPolicy rewrites the @mcp records of a schema the core loaded
// the way a loader with ReviewPolicy registered writes them: a tool on the
// core default gets ReviewPolicy's default, and one that asks is always
// reviewed.
func UseReviewPolicy(t *testing.T, schema *ir.Schema) {
	t.Helper()
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			if op.MCP == nil || op.MCP.Hidden {
				continue
			}
			switch op.MCP.Invocation.Value {
			case apigen.ToolInvocationAuto:
				op.MCP.Invocation = ir.MCPInvocation{Key: "review", Value: "on-write"}
			case apigen.ToolInvocationAsk:
				op.MCP.Invocation = ir.MCPInvocation{Key: "review", Value: "always"}
			default:
				t.Fatalf("%s.%s has invocation policy %+v", set.Name, op.Name, op.MCP.Invocation)
			}
		}
	}
}

// replacements turn a tool document written under the core policy into
// the same document under ReviewPolicy, as UseReviewPolicy maps the values.
var replacements = []string{
	`"invocationPolicy": "auto"`, `"review": "on-write"`,
	`"invocationPolicy": "ask"`, `"review": "always"`,
	`"invocationPolicy": ""`, `"review": ""`,
	`invocationPolicy: 'auto'`, `review: 'on-write'`,
	`invocationPolicy: 'ask'`, `review: 'always'`,
	`invocationPolicy?: 'auto' | 'ask';`, `review?: 'never' | 'on-write' | 'always';`,
}

// CompareUnderReviewPolicy checks that a document written under
// ReviewPolicy is the core document with the policy's key and values
// swapped and nothing else changed, and that the core document carries the
// policy at all.
func CompareUnderReviewPolicy(t *testing.T, name, core, review string) {
	t.Helper()
	if !strings.Contains(core, "invocationPolicy") {
		t.Fatalf("%s: the core document carries no invocation policy", name)
	}
	want := strings.NewReplacer(replacements...).Replace(core)
	if strings.Contains(want, "invocationPolicy") {
		t.Fatalf("%s: a core policy line has no replacement:\n%s", name, want)
	}
	if review != want {
		t.Fatalf("%s under the review policy is not the core document with the policy swapped\ngot:\n%s\nwant:\n%s", name, review, want)
	}
}
