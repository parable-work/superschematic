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

// renderBehaviors is the behavior catalog: each core behavior @behavior
// composes as the registry declares it, what it adds to a type and the
// name of its config type, then every config type, from
// @superschematic/schema. A behavior the behaviors' core alone implements
// (registry.RulesPackage: Seats, Reconcile, Schedules, Pool, D104;
// Recurrence, D152; Aliased, D147) is left out: only a table composes it,
// so BehaviorConfigs names no type for it, and the guide to tables
// (database-tables) describes it. The behaviors D103 folds into schema
// features (Links, Search, Queue and Branches) are no registered ones since
// the engine went (D103, #587).
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
	b.WriteString("# Behaviors\n\n")
	b.WriteString("A behavior adds fields, operations, checks and storage to a type. A\n")
	b.WriteString("General schema's type composes one with `@behavior(\"Workflow\", { ... })`\n")
	b.WriteString("from `@superschematic/schema`, once per behavior, and development mode\n")
	b.WriteString("serves it (D117). A DB type takes no `@behavior`: it is a table, which\n")
	b.WriteString("composes a behavior by a field type, `status: Workflow<{ ... }>`, or by\n")
	b.WriteString("`implements Timers<Ticket, { ... }>` (D103). The config is checked\n")
	b.WriteString("against the behavior's declaration when the schema loads. A read returns\n")
	b.WriteString("a behavior's fields under `behaviors.<Behavior>`, beside the type's own\n")
	b.WriteString("data; elsewhere a field is named by its qualified name,\n")
	b.WriteString("`Workflow.status`.\n\n")
	b.WriteString("Every type that composes a behavior with `@behavior` also has the create,\n")
	b.WriteString("get, list, update and delete operations, which the lists below leave\n")
	b.WriteString("out. An extension's\n")
	b.WriteString("behaviors are named `<extension>.<Name>`, and their configs are given as\n")
	b.WriteString("the JSON Schema the declaration checks them against.\n")

	for _, beh := range reg.Behaviors() {
		if !registry.EngineImplements(beh) {
			continue
		}
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
		// A core behavior's package names the engine's, which D103's
		// step 7 deleted: the behaviors' core implements it.
		if beh.Extension != "" && beh.Package != "" {
			fmt.Fprintf(&b, "- Implemented by `%s`.\n", beh.Package)
		}
		if len(beh.Fields) > 0 {
			b.WriteString("\nFields:\n\n")
			for _, f := range beh.Fields {
				fmt.Fprintf(&b, "- `%s`", registry.BehaviorFieldPath(beh.Name, f.Name))
				switch {
				case f.PerReader:
					b.WriteString(" (volatile, per reader: computed for each caller when read, and no event records it)")
				case f.Volatile:
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
