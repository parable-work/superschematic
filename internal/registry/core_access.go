package registry

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// accessDecorators returns the per-record access decorators of
// @superschematic/db (D156), each only in DB schemas: @audience on a table,
// @accessGrants on the class that is the table of grants, and @accessLog on
// the class that is the access log. Apply reads each declaration's shape;
// ir.Schema.Validate checks what the declaration alone decides, and the
// loader's verification pass checks it against the fields and the tables.
func accessDecorators() []DecoratorSpec {
	return []DecoratorSpec{
		{
			Name: "audience", Packages: []string{pkgDB}, Target: TargetType,
			Kinds: []string{string(ir.SchemaKindDB)},
			Apply: func(n Node, args []any, _ Site) error {
				if n.Type.Audience != nil {
					return fmt.Errorf("a class takes one @audience")
				}
				audience, err := audienceOf(n.Type.Name, args)
				if err != nil {
					return err
				}
				n.Type.Audience = audience
				return nil
			},
		},
		{
			Name: "accessGrants", Packages: []string{pkgDB}, Target: TargetType,
			Kinds: []string{string(ir.SchemaKindDB)},
			Apply: func(n Node, args []any, _ Site) error {
				if len(args) != 0 {
					return fmt.Errorf("@accessGrants on %s takes no arguments: the loader fills the class's fields", n.Type.Name)
				}
				n.Type.AccessGrants = &ir.AccessGrantsDef{}
				return nil
			},
		},
		{
			Name: "accessLog", Packages: []string{pkgDB}, Target: TargetType,
			Kinds: []string{string(ir.SchemaKindDB)},
			Apply: func(n Node, args []any, _ Site) error {
				if len(args) != 0 {
					return fmt.Errorf("@accessLog on %s takes no arguments: the loader fills the class's fields", n.Type.Name)
				}
				n.Type.AccessLog = &ir.AccessLogDef{}
				return nil
			},
		},
	}
}

// audienceOf reads @audience<T>({ owner, inheritFrom, default, agents,
// queue }) on the class named typeName. No argument is every option's
// default.
func audienceOf(typeName string, args []any) (*ir.AudienceDef, error) {
	const shape = "@audience<T>({ owner?, inheritFrom?, default?, agents?, queue? })"
	audience := &ir.AudienceDef{}
	if len(args) == 0 {
		return audience, nil
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("@audience on %s takes at most one options object: %s", typeName, shape)
	}
	obj, ok := args[0].(map[string]any)
	if !ok {
		return nil, ArgErrorf(0, "@audience on %s takes an options object: %s", typeName, shape)
	}
	for _, key := range sortedKeys(obj) {
		if key == "queue" {
			queue, err := classOrName(obj[key])
			if err != nil {
				return nil, ArgErrorf(0, "@audience on %s: queue is the @queue class a parent's change of audience enqueues its children's resolving on: %v", typeName, err)
			}
			audience.Queue = queue
			continue
		}
		value, ok := obj[key].(string)
		if !ok || value == "" {
			return nil, ArgErrorf(0, "@audience on %s: %s is a non-empty string; got %v", typeName, key, obj[key])
		}
		switch key {
		case "owner":
			audience.Owner = value
		case "inheritFrom":
			audience.InheritFrom = value
		case "default":
			audience.Default = value
		case "agents":
			audience.Agents = value
		default:
			return nil, ArgErrorf(0, "@audience on %s has no option %q; it takes owner, inheritFrom, default, agents and queue", typeName, key)
		}
	}
	return audience, nil
}
