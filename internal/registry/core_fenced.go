package registry

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// fencedDecorator returns @fenced, from @superschematic/schema and only in
// DB schemas: an integer field of the class's table that every write the
// fence covers advances by one (D119). Apply reads the declaration's
// shape. ir.Schema.Validate checks what the declarations alone decide, and
// verification checks them against the fields, the table and the service,
// in every authoring form.
func fencedDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "fenced", Packages: []string{pkgSchema}, Target: TargetType,
		Kinds: []string{string(ir.SchemaKindDB)},
		Apply: func(n Node, args []any, _ Site) error {
			fence, err := fenceOf(n.Type.Name, args)
			if err != nil {
				return err
			}
			n.Type.Fences = append(n.Type.Fences, fence)
			return nil
		},
	}
}

// fenceOf reads @fenced<T>({ field, covers }) on the class named typeName.
func fenceOf(typeName string, args []any) (*ir.Fence, error) {
	const shape = "@fenced<T>({ field, covers })"
	if len(args) != 1 {
		return nil, fmt.Errorf("@fenced on %s takes one options object: %s", typeName, shape)
	}
	obj, ok := args[0].(map[string]any)
	if !ok {
		return nil, ArgErrorf(0, "@fenced on %s takes an options object: %s", typeName, shape)
	}
	field, ok := obj["field"].(string)
	if !ok || field == "" {
		return nil, ArgErrorf(0, "@fenced on %s names no field; name the integer field every covered write advances, as field: \"version\"", typeName)
	}
	where := fmt.Sprintf("@fenced on %s.%s", typeName, field)
	for _, key := range sortedKeys(obj) {
		switch key {
		case "field", "covers":
		default:
			return nil, ArgErrorf(0, "%s has no option %q; it takes field and covers", where, key)
		}
	}

	fence := &ir.Fence{Field: field}
	covers, present := obj["covers"]
	if !present {
		return fence, nil
	}
	list, ok := covers.([]any)
	if !ok {
		return nil, ArgErrorf(0, "%s: covers is a list of the fields of %s whose change the fence counts, as [\"label\", \"owner\"]; got %v", where, typeName, covers)
	}
	for _, entry := range list {
		name, ok := entry.(string)
		if !ok {
			return nil, ArgErrorf(0, "%s: covers lists %v; name a field of %s, as \"label\"", where, entry, typeName)
		}
		fence.Covers = append(fence.Covers, name)
	}
	// An empty list counts nothing, and the IR's JSON would leave it out,
	// so a schema read back would hold a fence that counts every update.
	if len(fence.Covers) == 0 {
		return nil, ArgErrorf(0, "%s: covers is empty, so the fence would count no write; list the fields whose change it counts, or leave covers out and it counts every update of the row", where)
	}
	return fence, nil
}
