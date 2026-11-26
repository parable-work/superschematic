package registry

import (
	"fmt"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// searchDecorator returns @searchable of @superschematic/db, only in DB
// schemas (D155): the field that titles a table's rows and the fields that
// hold their body, which the database's search index holds, and the queue
// a write that changes them enqueues on. Apply reads the declaration's
// shape; ir.Schema.Validate checks what it alone decides, and the loader's
// verification pass checks the fields and the queue it names.
func searchDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "searchable", Packages: []string{pkgDB}, Target: TargetType,
		Kinds: []string{string(ir.SchemaKindDB)},
		Apply: func(n Node, args []any, _ Site) error {
			if n.Type.Searchable != nil {
				return fmt.Errorf("a class takes one @searchable")
			}
			def, err := searchableOf(n.Type.Name, args)
			if err != nil {
				return err
			}
			n.Type.Searchable = def
			return nil
		},
	}
}

// searchableOf reads @searchable<T>({ title, body?, scope?, queue,
// comments?, attachments? }) on the class named typeName. body is a field
// or a list of them.
func searchableOf(typeName string, args []any) (*ir.SearchableDef, error) {
	const shape = "@searchable<T>({ title, body?, scope?, queue, comments?, attachments? })"
	if len(args) != 1 {
		return nil, fmt.Errorf("@searchable on %s takes one options object, which names the title and the queue: %s", typeName, shape)
	}
	obj, ok := args[0].(map[string]any)
	if !ok {
		return nil, ArgErrorf(0, "@searchable on %s takes an options object: %s", typeName, shape)
	}
	def := &ir.SearchableDef{}
	field := func(key string, value any) (string, error) {
		s, ok := value.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return "", ArgErrorf(0, "@searchable on %s: %s is a field of the table, by name; got %v", typeName, key, value)
		}
		return s, nil
	}
	flag := func(key string, value any) (*bool, error) {
		b, ok := value.(bool)
		if !ok {
			return nil, ArgErrorf(0, "@searchable on %s: %s is true or false; got %v", typeName, key, value)
		}
		return &b, nil
	}
	for _, key := range sortedKeys(obj) {
		value := obj[key]
		var err error
		switch key {
		case "title":
			if def.Title, err = field(key, value); err != nil {
				return nil, err
			}
		case "body":
			if s, ok := value.(string); ok {
				if def.Body = []string{s}; strings.TrimSpace(s) == "" {
					return nil, ArgErrorf(0, "@searchable on %s: body is a field of the table or a list of them", typeName)
				}
				continue
			}
			if def.Body, err = stringList(value); err != nil || len(def.Body) == 0 {
				return nil, ArgErrorf(0, "@searchable on %s: body is a field of the table or a list of them, as [\"summary\", \"text\"]", typeName)
			}
		case "scope":
			if def.Scope, err = field(key, value); err != nil {
				return nil, err
			}
		case "queue":
			if def.Queue, err = classOrName(value); err != nil {
				return nil, ArgErrorf(0, "@searchable on %s: queue is the @queue class a write that changes the indexed text enqueues on: %v", typeName, err)
			}
		case "comments":
			if def.Comments, err = flag(key, value); err != nil {
				return nil, err
			}
		case "attachments":
			if def.Attachments, err = flag(key, value); err != nil {
				return nil, err
			}
		default:
			return nil, ArgErrorf(0, "@searchable on %s has no option %q; it takes title, body, scope, queue, comments and attachments", typeName, key)
		}
	}
	if def.Title == "" || def.Queue == "" {
		return nil, ArgErrorf(0, "@searchable on %s needs title and queue: %s", typeName, shape)
	}
	return def, nil
}
