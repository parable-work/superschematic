package registry

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// seedRowsDecorator returns @seedRows, from @superschematic/db and only in
// DB schemas: the rows the class's table holds from its first migration
// on (#625, #667), each its fields' values by name, and the fields that
// identify one (key). Apply reads the shape, once per class; verification
// checks the table, the fields and the key, and the loader each value
// against its field.
func seedRowsDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "seedRows", Packages: []string{pkgDB}, Target: TargetType,
		Kinds: []string{string(ir.SchemaKindDB)},
		Apply: func(n Node, args []any, _ Site) error {
			if n.Type.SeedRows != nil {
				return fmt.Errorf("@seedRows of %s is declared twice; list every row in one", n.Type.Name)
			}
			seeds, err := seedRowsOf(args)
			if err != nil {
				return err
			}
			n.Type.SeedRows = seeds
			return nil
		},
	}
}

// seedRowsOf reads @seedRows([{ field: value, ... }, ...], { key? }).
func seedRowsOf(args []any) (*ir.SeedRows, error) {
	if len(args) != 1 && len(args) != 2 {
		return nil, fmt.Errorf(`@seedRows takes a list of rows and, optionally, { key }: @seedRows([{ channelKind: "sms", level: "low" }], { key: ["channelKind"] })`)
	}
	list, ok := args[0].([]any)
	if !ok || len(list) == 0 {
		return nil, ArgErrorf(0, "@seedRows takes a list of one or more rows, each an object of a row's fields")
	}
	seeds := &ir.SeedRows{Rows: make([]map[string]any, 0, len(list))}
	for i, item := range list {
		row, ok := item.(map[string]any)
		if !ok || len(row) == 0 {
			return nil, ArgErrorf(0, "@seedRows row %d is %v; write an object of the row's fields, { field: value }", i, item)
		}
		for field, value := range row {
			if err := seedValue(value, 0); err != nil {
				return nil, ArgErrorf(0, "@seedRows row %d's %s: %v", i, field, err)
			}
		}
		seeds.Rows = append(seeds.Rows, row)
	}
	if len(args) == 2 {
		options, ok := args[1].(map[string]any)
		if !ok {
			return nil, ArgErrorf(1, "@seedRows's options are %v; write { key: [\"field\"] }", args[1])
		}
		for name, value := range options {
			switch name {
			case "key":
				key, err := stringList(value)
				if err != nil || len(key) == 0 {
					return nil, ArgErrorf(1, "@seedRows's key names the fields that identify a seeded row, a list of one or more: %v", value)
				}
				seeds.Key = key
			default:
				return nil, ArgErrorf(1, "@seedRows has no option %q; it takes key", name)
			}
		}
	}
	return seeds, nil
}

// seedValue checks a seeded field's value is one the IR holds as JSON: a
// string, a number, a boolean, null, a list of values or an object of
// them. A class named as a value is no value a column holds.
func seedValue(value any, depth int) error {
	if depth > 32 {
		return fmt.Errorf("the value nests too deeply")
	}
	switch v := value.(type) {
	case nil, string, bool, float64, int, int64:
		return nil
	case []any:
		for _, item := range v {
			if err := seedValue(item, depth+1); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		if _, isClass := v["class"]; isClass && len(v) == 1 {
			return fmt.Errorf("names a class; a seeded field holds a value")
		}
		for _, item := range v {
			if err := seedValue(item, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("%v is not a value a column holds", value)
}
