package registry

import (
	"fmt"
	"maps"
	"slices"

	ir "github.com/parable-work/superschematic/ir"
)

// rowFeatureDecorators returns @references and @attachments, from
// @superschematic/db and only in DB schemas (D154): the rows a table's row
// points at, and the files it holds in a bucket, each in a sub-table the
// loader adds. Apply reads each declaration's shape; ir.Schema.Validate
// checks what the declaration alone decides, and the loader's verification
// pass checks the tables and queues it names.
func rowFeatureDecorators() []DecoratorSpec {
	return []DecoratorSpec{
		{
			Name: "references", Packages: []string{pkgDB}, Target: TargetType,
			Kinds: []string{string(ir.SchemaKindDB)},
			Apply: func(n Node, args []any, _ Site) error {
				if n.Type.RowReferences != nil {
					return fmt.Errorf("a class takes one @references")
				}
				def, err := rowReferencesOf(n.Type.Name, args)
				if err != nil {
					return err
				}
				n.Type.RowReferences = def
				return nil
			},
		},
		{
			Name: "attachments", Packages: []string{pkgDB}, Target: TargetType,
			Kinds: []string{string(ir.SchemaKindDB)},
			Apply: func(n Node, args []any, _ Site) error {
				if n.Type.Attachments != nil {
					return fmt.Errorf("a class takes one @attachments")
				}
				def, err := attachmentsOf(n.Type.Name, args)
				if err != nil {
					return err
				}
				n.Type.Attachments = def
				return nil
			},
		},
	}
}

// classOrName reads a class named as a value, or its name as a string.
func classOrName(value any) (string, error) {
	if s, ok := value.(string); ok && s != "" {
		return s, nil
	}
	return DecodeClassRef(value)
}

// rowReferencesOf reads @references<T>({ tables, cleared, maxPerRow }) on
// the class named typeName. No argument is every option's default: any
// table, no cleared queue.
func rowReferencesOf(typeName string, args []any) (*ir.RowReferencesDef, error) {
	const shape = "@references<T>({ tables?, cleared?, maxPerRow? })"
	def := &ir.RowReferencesDef{}
	if len(args) == 0 {
		return def, nil
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("@references on %s takes at most one options object: %s", typeName, shape)
	}
	obj, ok := args[0].(map[string]any)
	if !ok {
		return nil, ArgErrorf(0, "@references on %s takes an options object: %s", typeName, shape)
	}
	for _, key := range slices.Sorted(maps.Keys(obj)) {
		value := obj[key]
		switch key {
		case "tables":
			list, ok := value.([]any)
			if !ok || len(list) == 0 {
				return nil, ArgErrorf(0, "@references on %s: tables is a list of one or more tables of the database, as [Ticket, Note]; leave it out for any table", typeName)
			}
			for _, item := range list {
				name, err := classOrName(item)
				if err != nil {
					return nil, ArgErrorf(0, "@references on %s: tables lists a table of the database by its class: %v", typeName, err)
				}
				def.Tables = append(def.Tables, name)
			}
		case "cleared":
			name, err := classOrName(value)
			if err != nil {
				return nil, ArgErrorf(0, "@references on %s: cleared is the @queue class a target's delete enqueues on: %v", typeName, err)
			}
			def.Cleared = name
		case "maxPerRow":
			n, ok := wholeNumber(value)
			if !ok || n < 1 {
				return nil, ArgErrorf(0, "@references on %s: maxPerRow is a whole number above 0; got %v", typeName, value)
			}
			def.MaxPerRow = n
		default:
			return nil, ArgErrorf(0, "@references on %s has no option %q; it takes tables, cleared and maxPerRow", typeName, key)
		}
	}
	return def, nil
}

// attachmentsOf reads @attachments<T>({ released, maxFileBytes,
// maxRowBytes, maxFiles, mediaTypes }) on the class named typeName.
func attachmentsOf(typeName string, args []any) (*ir.AttachmentsDef, error) {
	const shape = "@attachments<T>({ released, maxFileBytes?, maxRowBytes?, maxFiles?, mediaTypes? })"
	if len(args) != 1 {
		return nil, fmt.Errorf("@attachments on %s takes one options object, which names the released queue: %s", typeName, shape)
	}
	obj, ok := args[0].(map[string]any)
	if !ok {
		return nil, ArgErrorf(0, "@attachments on %s takes an options object: %s", typeName, shape)
	}
	def := &ir.AttachmentsDef{}
	size := func(key string, value any) (int64, error) {
		n, ok := value.(float64)
		if !ok || n < 1 || n != float64(int64(n)) {
			return 0, ArgErrorf(0, "@attachments on %s: %s is a whole number of bytes above 0; got %v", typeName, key, value)
		}
		return int64(n), nil
	}
	for _, key := range slices.Sorted(maps.Keys(obj)) {
		value := obj[key]
		var err error
		switch key {
		case "released":
			if def.Released, err = classOrName(value); err != nil {
				return nil, ArgErrorf(0, "@attachments on %s: released is the @queue class a file's delete enqueues on: %v", typeName, err)
			}
		case "maxFileBytes":
			if def.MaxFileBytes, err = size(key, value); err != nil {
				return nil, err
			}
		case "maxRowBytes":
			if def.MaxRowBytes, err = size(key, value); err != nil {
				return nil, err
			}
		case "maxFiles":
			n, ok := wholeNumber(value)
			if !ok || n < 1 {
				return nil, ArgErrorf(0, "@attachments on %s: maxFiles is a whole number above 0; got %v", typeName, value)
			}
			def.MaxFiles = n
		case "mediaTypes":
			if def.MediaTypes, err = stringList(value); err != nil || len(def.MediaTypes) == 0 {
				return nil, ArgErrorf(0, "@attachments on %s: mediaTypes is a list of one or more media types or ranges, as [\"image/*\", \"application/pdf\"]", typeName)
			}
		default:
			return nil, ArgErrorf(0, "@attachments on %s has no option %q; it takes released, maxFileBytes, maxRowBytes, maxFiles and mediaTypes", typeName, key)
		}
	}
	if def.Released == "" {
		return nil, ArgErrorf(0, "@attachments on %s names no released queue: %s", typeName, shape)
	}
	return def, nil
}
