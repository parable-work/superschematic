package schemafile

import (
	"encoding/json"
	"strings"
	"testing"
)

// coreDefinition is the core registry's JSON Schema as a map the tests
// edit before rendering it.
func coreDefinition(t *testing.T) map[string]any {
	t.Helper()
	data, err := Definition()
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	return root
}

func defProperties(root map[string]any, def string) map[string]any {
	return root["$defs"].(map[string]any)[def].(map[string]any)["properties"].(map[string]any)
}

// TestTypeScriptDeclarationsRefuseWhatTheyCannotExpress: the emitter reads
// the JSON Schema subset the reflection produces. A keyword or shape outside
// it, or a schema that lost a part a registry closes, fails with where it
// sits instead of writing a looser type.
func TestTypeScriptDeclarationsRefuseWhatTheyCannotExpress(t *testing.T) {
	policyKey := core().ToolInvocationPolicy().Key
	kinds := core().Kinds()
	behaviors := core().BehaviorNames()
	if _, err := renderDeclarations(coreDefinition(t), kinds, policyKey, behaviors); err != nil {
		t.Fatalf("the core schema: %v", err)
	}

	for name, tc := range map[string]struct {
		edit func(root map[string]any)
		want string
	}{
		"a keyword outside the subset": {
			edit: func(root map[string]any) {
				defProperties(root, "TypeRef")["name"].(map[string]any)["pattern"] = "^[A-Z]"
			},
			want: `$defs/TypeRef/properties/name: unsupported JSON Schema keyword "pattern"`,
		},
		"a type union": {
			edit: func(root map[string]any) {
				defProperties(root, "TypeRef")["name"] = map[string]any{"type": []any{"string", "null"}}
			},
			want: "$defs/TypeRef/properties/name: unsupported schema type",
		},
		"a $ref outside $defs": {
			edit: func(root map[string]any) {
				defProperties(root, "FieldDef")["typeRef"] = map[string]any{"$ref": "other.json#/TypeRef"}
			},
			want: "$defs/FieldDef/properties/typeRef: $ref other.json#/TypeRef does not name a $defs entry",
		},
		"an open definition": {
			edit: func(root map[string]any) {
				delete(root["$defs"].(map[string]any)["TypeRef"].(map[string]any), "additionalProperties")
			},
			want: "$defs/TypeRef: a definition must be a closed object",
		},
		"a closed map outside a registry's slots": {
			edit: func(root map[string]any) {
				defProperties(root, "TraitRef")["configArgs"] = map[string]any{"type": "object", "additionalProperties": false}
			},
			want: "$defs/TraitRef/properties/configArgs: a closed object with no properties is only written for a registry's slots",
		},
		"no invocation policy": {
			edit: func(root map[string]any) {
				delete(defProperties(root, "OperationMCP"), policyKey)
			},
			want: `$defs/OperationMCP has no "invocationPolicy" property`,
		},
		"kinds that are not the registry's": {
			edit: func(root map[string]any) {
				defProperties(root, "Document")["kind"].(map[string]any)["enum"] = []any{"General"}
			},
			want: "$defs/Document/properties/kind: expected the enum of the registry's kinds",
		},
		"no extensions slot on a type": {
			edit: func(root map[string]any) {
				delete(defProperties(root, "TypeDef"), "extensions")
			},
			want: "$defs/TypeDef has no extensions property",
		},
		"behavior names that are not the registry's": {
			edit: func(root map[string]any) {
				defProperties(root, "BehaviorRef")["name"].(map[string]any)["enum"] = []any{"acme.Stock"}
			},
			want: "$defs/BehaviorRef/properties/name: expected a string enum of the registry's behaviors",
		},
		"a behavior config closed in the core": {
			edit: func(root map[string]any) {
				defProperties(root, "BehaviorRef")["config"].(map[string]any)["type"] = "object"
			},
			want: `$defs/BehaviorRef/properties/config: unsupported JSON Schema keyword "type"`,
		},
		"a behavior list limited while the core has behaviors": {
			edit: func(root map[string]any) {
				defProperties(root, "TypeDef")["behaviors"].(map[string]any)["maxItems"] = 0
			},
			want: "$defs/TypeDef/properties/behaviors: expected a list of BehaviorRef, with maxItems 0 when the registry has no behavior",
		},
		"a behavior's config branch missing": {
			edit: func(root map[string]any) {
				ref := root["$defs"].(map[string]any)["BehaviorRef"].(map[string]any)
				ref["allOf"] = ref["allOf"].([]any)[1:]
			},
			want: "$defs/BehaviorRef/allOf: expected one branch per registered behavior [Comments Revisions Workflow]",
		},
		"a config branch for another behavior": {
			edit: func(root map[string]any) {
				branch := root["$defs"].(map[string]any)["BehaviorRef"].(map[string]any)["allOf"].([]any)[0].(map[string]any)
				branch["if"].(map[string]any)["properties"].(map[string]any)["name"].(map[string]any)["const"] = "acme.Stock"
			},
			want: "$defs/BehaviorRef/allOf/0: expected the branch that holds the config of behavior Comments",
		},
		"no behavior config": {
			edit: func(root map[string]any) {
				delete(defProperties(root, "BehaviorRef"), "config")
			},
			want: "$defs/BehaviorRef has no name or no config property",
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := coreDefinition(t)
			tc.edit(root)
			_, err := renderDeclarations(root, kinds, policyKey, behaviors)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
