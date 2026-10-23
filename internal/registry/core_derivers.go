package registry

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// deriverDecorator returns @deriver, from @superschematic/api and only in
// API schemas: a class of the schema that declares a deriver of the API,
// which fills the Derived fields of engine types of the API's engine
// database (D74). The class holds no fields and is no type: Apply records
// the deriver in Schema.Derivers, and the TypeScript reader leaves the
// class out of Types. Each of its schemas is the name of an engine type of
// the API's database, a string: an API imports no engine type, which its
// generators do not render (D58).
func deriverDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "deriver", Packages: []string{pkgAPI}, Target: TargetType,
		Kinds: []string{string(ir.SchemaKindAPI)},
		Apply: func(n Node, args []any, _ Site) error {
			deriver, err := deriverOf(n.Type, args)
			if err != nil {
				return err
			}
			if n.Schema.Deriver(deriver.Name) != nil {
				return fmt.Errorf("@deriver class %s is declared twice", deriver.Name)
			}
			n.Schema.Derivers = append(n.Schema.Derivers, deriver)
			return nil
		},
	}
}

// deriverOf reads @deriver({ schemas, concurrency, batchSize }) on td.
func deriverOf(td *ir.TypeDef, args []any) (*ir.Deriver, error) {
	if len(td.Fields) > 0 {
		return nil, fmt.Errorf("@deriver class %s has fields; a deriver's inputs are its schemas' fields, so its class holds none", td.Name)
	}
	if td.Extends != "" || len(td.Implements) > 0 {
		return nil, fmt.Errorf("@deriver class %s extends or implements another class; a deriver's class declares the deriver and nothing else", td.Name)
	}
	if len(args) > 1 {
		return nil, fmt.Errorf("@deriver takes one options object: { schemas, concurrency, batchSize }")
	}
	deriver := &ir.Deriver{Name: td.Name, Comment: td.Comment}
	if len(args) == 0 || args[0] == nil {
		return deriver, nil
	}
	obj, ok := args[0].(map[string]any)
	if !ok {
		return nil, ArgErrorf(0, "@deriver takes an options object: { schemas, concurrency, batchSize }")
	}
	for key, value := range obj {
		switch key {
		case "schemas":
			list, ok := value.([]any)
			if !ok {
				return nil, ArgErrorf(0, "@deriver schemas is a list of the engine types whose Derived fields the deriver fills, imported from the API's database")
			}
			for _, item := range list {
				name, ok := item.(string)
				if !ok {
					return nil, ArgErrorf(0, "@deriver schemas lists the names of engine types of the API's database that compose Derived, as strings (\"Ticket\"); an API imports no engine type")
				}
				deriver.Schemas = append(deriver.Schemas, name)
			}
		case "concurrency":
			n, ok := value.(float64)
			if !ok || n != float64(int(n)) || n < 1 {
				return nil, ArgErrorf(0, "@deriver concurrency is %v; it is a whole number, one or more", value)
			}
			deriver.Concurrency = int(n)
		case "batchSize":
			n, ok := value.(float64)
			if !ok || n != float64(int(n)) || n < 1 || n > ir.MaxDeriverBatchSize {
				return nil, ArgErrorf(0, "@deriver batchSize is %v; it is a whole number from 1 to %d", value, ir.MaxDeriverBatchSize)
			}
			deriver.BatchSize = int(n)
		default:
			return nil, ArgErrorf(0, "@deriver has no option %q; it takes schemas, concurrency and batchSize", key)
		}
	}
	if err := CheckDeriver(deriver); err != nil {
		return nil, ArgErrorf(0, "@deriver %s: %v", deriver.Name, err)
	}
	return deriver, nil
}

// CheckDeriver checks a deriver's declaration in every form: a name, each
// schema named once, a concurrency of one or more when set, and a batch
// size from 1 to ir.MaxDeriverBatchSize when set. Whether each schema is
// an engine type of the API's database that composes Derived is checked
// where that database's schema is read: the API's build and the stack's
// resolution.
func CheckDeriver(deriver *ir.Deriver) error {
	switch {
	case deriver.Name == "":
		return fmt.Errorf("a deriver has no name")
	case deriver.Concurrency < 0:
		return fmt.Errorf("concurrency is %d; it is one or more", deriver.Concurrency)
	case deriver.BatchSize < 0 || deriver.BatchSize > ir.MaxDeriverBatchSize:
		return fmt.Errorf("batchSize is %d; it is from 1 to %d", deriver.BatchSize, ir.MaxDeriverBatchSize)
	}
	seen := map[string]bool{}
	for _, schema := range deriver.Schemas {
		switch {
		case schema == "":
			return fmt.Errorf("names a schema with no name")
		case seen[schema]:
			return fmt.Errorf("names schema %s twice", schema)
		}
		seen[schema] = true
	}
	return nil
}

// DeriverConcurrency is a checked deriver's concurrency, its default when
// it sets none.
func DeriverConcurrency(deriver *ir.Deriver) int {
	if deriver.Concurrency > 0 {
		return deriver.Concurrency
	}
	return ir.DefaultDeriverConcurrency
}

// DeriverBatchSize is a checked deriver's batch size, its default when it
// sets none.
func DeriverBatchSize(deriver *ir.Deriver) int {
	if deriver.BatchSize > 0 {
		return deriver.BatchSize
	}
	return ir.DefaultDeriverBatchSize
}
