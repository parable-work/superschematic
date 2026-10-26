package agentcatalog

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
)

// behaviorConfigs is the interface of @superschematic/schema that maps
// each core behavior's name to the type of the config a type gives it.
const behaviorConfigs = "BehaviorConfigs"

// configMember is one `Name: ConfigType;` member of BehaviorConfigs.
var configMember = regexp.MustCompile(`(?m)^\s*(?:readonly\s+)?([A-Za-z_$][\w$]*)\??:\s*([A-Za-z_$][\w$]*)`)

// renderBehaviors is the behavior catalog: each core behavior as the
// registry declares it, what it adds to a type and the name of its config
// type, then every config type, from @superschematic/schema.
func renderBehaviors(exports []export, reg *registry.Registry) ([]byte, error) {
	configTypes := map[string]string{}
	for _, d := range typeDecls(exports) {
		if d.name != behaviorConfigs {
			continue
		}
		for _, m := range configMember.FindAllStringSubmatch(stripCommentsAndStrings(d.text), -1) {
			configTypes[m[1]] = m[2]
		}
	}
	if len(configTypes) == 0 {
		return nil, fmt.Errorf("%s: no %s interface with members", schemaPackage, behaviorConfigs)
	}

	var b bytes.Buffer
	b.WriteString("# Engine behaviors\n\n")
	b.WriteString("A behavior adds fields, operations, checks and storage to a type that an\n")
	b.WriteString("engine runs (`@superschematic/engine`, in TypeScript or Rust): an engine\n")
	b.WriteString("type, which keeps no table and gets no ORM. A type composes one with\n")
	b.WriteString("`@behavior(\"Workflow\", { ... })` from `@superschematic/schema`, once per\n")
	b.WriteString("behavior, and the config is checked against the behavior's declaration\n")
	b.WriteString("when the schema loads. A read returns a behavior's fields under\n")
	b.WriteString("`behaviors.<Behavior>`, beside the type's own data; elsewhere a field is\n")
	b.WriteString("named by its qualified name, `Workflow.status`.\n\n")
	b.WriteString("Every type an engine runs also has the create, get, list, update and\n")
	b.WriteString("delete operations, which the lists below leave out. An extension's\n")
	b.WriteString("behaviors are named `<extension>.<Name>`, and their configs are given as\n")
	b.WriteString("the JSON Schema the declaration checks them against.\n")

	for _, beh := range reg.Behaviors() {
		fmt.Fprintf(&b, "\n## `%s`\n\n", beh.Name)
		if beh.Description != "" {
			b.WriteString(cell(beh.Description) + "\n\n")
		}
		switch config, ok := configTypes[beh.Name]; {
		case len(beh.ConfigSchema) == 0:
			b.WriteString("- Takes no config.\n")
		case beh.Extension != "":
			schema, err := compactJSON(beh.ConfigSchema)
			if err != nil {
				return nil, fmt.Errorf("behavior %s: %w", beh.Name, err)
			}
			required := "optional"
			if beh.ConfigRequired() {
				required = "required"
			}
			fmt.Fprintf(&b, "- Registered by the `%s` extension. Config, %s: %s\n", beh.Extension, required, schema)
		case !ok:
			return nil, fmt.Errorf("behavior %s takes a config, but %s names no type for it", beh.Name, behaviorConfigs)
		case beh.ConfigRequired():
			fmt.Fprintf(&b, "- Config: `%s`, required.\n", config)
		default:
			fmt.Fprintf(&b, "- Config: `%s`, optional.\n", config)
		}
		if len(beh.Requires) > 0 {
			fmt.Fprintf(&b, "- Requires %s on the same type.\n", codeList(beh.Requires))
		}
		if len(beh.Conflicts) > 0 {
			fmt.Fprintf(&b, "- Cannot be composed with %s.\n", codeList(beh.Conflicts))
		}
		if beh.Package != "" {
			fmt.Fprintf(&b, "- Implemented by `%s`.\n", beh.Package)
		}
		if len(beh.Fields) > 0 {
			b.WriteString("\nFields:\n\n")
			for _, f := range beh.Fields {
				fmt.Fprintf(&b, "- `%s`", registry.BehaviorFieldPath(beh.Name, f.Name))
				if f.Volatile {
					b.WriteString(" (volatile: computed when read)")
				}
				if f.Description != "" {
					b.WriteString(": " + cell(f.Description))
				}
				b.WriteString("\n")
			}
		}
		if len(beh.Operations) > 0 {
			b.WriteString("\nOperations:\n\n")
			for _, op := range beh.Operations {
				var traits []string
				if op.Writes {
					traits = append(traits, "writes")
				} else {
					traits = append(traits, "reads")
				}
				switch op.Scope {
				case registry.OperationScopeSchema:
					traits = append(traits, "on the schema")
				case registry.OperationScopeNamespace:
					traits = append(traits, "across the namespace's schemas, called by the behavior's name")
				default:
					traits = append(traits, "on an instance")
				}
				if op.Beside {
					traits = append(traits, "beside the instance")
					if !op.Logged() {
						traits = append(traits, "no event")
					}
				}
				if op.AsksRead() {
					traits = append(traits, "asks read")
				}
				fmt.Fprintf(&b, "- `%s` (%s)", op.Name, strings.Join(traits, ", "))
				if op.Description != "" {
					b.WriteString(": " + cell(op.Description))
				}
				b.WriteString("\n")
			}
		}
		if len(beh.Signals) > 0 {
			b.WriteString("\nSignals (delivered to event streams opened with `signals=1`, never stored):\n\n")
			for _, signal := range beh.Signals {
				sender := "callers who may " + signal.Access
				if signal.Access == registry.SignalAccessOperation {
					sender = "its operations alone"
				}
				fmt.Fprintf(&b, "- `%s` (sent by %s)", signal.Name, sender)
				if signal.Description != "" {
					b.WriteString(": " + cell(signal.Description))
				}
				b.WriteString("\n")
			}
		}
		if len(beh.Vetoes) > 0 {
			codes := make([]string, len(beh.Vetoes))
			for i, v := range beh.Vetoes {
				codes[i] = v.Code
			}
			fmt.Fprintf(&b, "\nRefusals carry the codes %s.\n", codeList(codes))
		}
	}

	b.WriteString("\n## Config types\n")
	include := closure(exports, []string{behaviorConfigs})
	for _, d := range typeDecls(exports) {
		if !include[d.name] {
			continue
		}
		fmt.Fprintf(&b, "\n```ts\n%s\n```\n", d.signature())
		if d.comment != "" {
			b.WriteString("\n" + d.comment + "\n")
		}
	}
	return b.Bytes(), nil
}
