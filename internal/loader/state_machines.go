package loader

import (
	ir "github.com/parable-work/superschematic/ir"
)

// orderStateMachines gives each @stateMachine's transitions the order of
// the members of the enum that types its field (D118), the schema's own
// or a dependency's, which the TypeScript reader hands over
// (verify.Input.ExternalEnums). The loader runs it on every authoring form
// before the verification pass: a TypeScript object's keys reach a
// decorator in no order, and a JSON or YAML file keeps the one its author
// wrote, so the IR, and every guard and helper written from it, is the
// same from each. A machine whose field or enum the schema lacks is left
// as it is, for the pass to refuse.
func orderStateMachines(schema *ir.Schema, externalEnums map[string]*ir.EnumDef) {
	for _, td := range schema.Types {
		if td == nil {
			continue
		}
		for _, machine := range td.StateMachines {
			if machine == nil {
				continue
			}
			for _, field := range td.Fields {
				if field == nil || field.Name != machine.Field {
					continue
				}
				enum := schema.Enums[field.TypeRef.Name]
				if enum == nil {
					enum = externalEnums[field.TypeRef.Name]
				}
				machine.OrderTransitions(enum)
			}
		}
	}
}
