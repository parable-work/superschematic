package registry

import (
	"fmt"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// calendarDecorators returns @calendarSource of @superschematic/db (D152),
// only in DB schemas, which a table repeats for each source: Apply reads
// each declaration's shape; the loader's verification pass checks it
// against the table's fields and its Recurrence.
func calendarDecorators() []DecoratorSpec {
	return []DecoratorSpec{
		{
			Name: "calendarSource", Packages: []string{pkgDB}, Target: TargetType,
			Kinds: []string{string(ir.SchemaKindDB)},
			Apply: func(n Node, args []any, _ Site) error {
				source, err := calendarSourceOf(n.Type.Name, args)
				if err != nil {
					return err
				}
				n.Type.CalendarSources = append(n.Type.CalendarSources, source)
				return nil
			},
		},
	}
}

// calendarSourceOf reads @calendarSource<T>({ name?, field?, end?,
// occurrences?, title, kind, color?, owner?, permission? }) on typeName.
func calendarSourceOf(typeName string, args []any) (ir.CalendarSourceDef, error) {
	const shape = "@calendarSource<T>({ name?, field?, end?, occurrences?, title, kind, color?, owner?, permission? })"
	var source ir.CalendarSourceDef
	if len(args) != 1 {
		return source, fmt.Errorf("@calendarSource on %s takes one options object: %s", typeName, shape)
	}
	obj, ok := args[0].(map[string]any)
	if !ok {
		return source, ArgErrorf(0, "@calendarSource on %s takes an options object: %s", typeName, shape)
	}
	for key, raw := range obj {
		if key == "occurrences" {
			flag, ok := raw.(bool)
			if !ok {
				return source, ArgErrorf(0, "@calendarSource on %s: occurrences is true or false; got %v", typeName, raw)
			}
			source.Occurrences = flag
			continue
		}
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return source, ArgErrorf(0, "@calendarSource on %s: %s is a non-empty string; got %v", typeName, key, raw)
		}
		switch key {
		case "name":
			source.Name = value
		case "field":
			source.Field = value
		case "end":
			source.End = value
		case "title":
			source.Title = value
		case "kind":
			source.Kind = value
		case "color":
			source.Color = value
		case "owner":
			source.Owner = value
		case "permission":
			source.Permission = value
		default:
			return source, ArgErrorf(0, "@calendarSource on %s has no option %q; it takes name, field, end, occurrences, title, kind, color, owner and permission", typeName, key)
		}
	}
	switch {
	case source.Title == "" || source.Kind == "":
		return source, ArgErrorf(0, "@calendarSource on %s needs title and kind: %s", typeName, shape)
	case source.Occurrences == (source.Field != ""):
		return source, ArgErrorf(0, "@calendarSource on %s dates its entries by field or by occurrences, one of the two", typeName)
	case source.Occurrences && source.End != "":
		return source, ArgErrorf(0, "@calendarSource on %s: an occurrence has its due alone, and takes no end", typeName)
	}
	return source, nil
}
