package registry

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// seedRolesDecorator returns @seedRoles, from @superschematic/db and only
// in DB schemas: the roles the class's table, a UserRole table, holds from
// its first migration on, each a name and its permissions (D88). Apply
// reads the list's shape, once per class; verification checks the table
// and each role against it, and an API that names the schema as its
// authDb checks the permissions against its own.
func seedRolesDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "seedRoles", Packages: []string{pkgDB}, Target: TargetType,
		Kinds: []string{string(ir.SchemaKindDB)},
		Apply: func(n Node, args []any, _ Site) error {
			if len(n.Type.SeedRoles) > 0 {
				return fmt.Errorf("@seedRoles of %s is declared twice; list every role in one", n.Type.Name)
			}
			seeds, err := roleSeedsOf(args)
			if err != nil {
				return err
			}
			n.Type.SeedRoles = seeds
			return nil
		},
	}
}

// roleSeedsOf reads @seedRoles([{ name, permissions }, ...]).
func roleSeedsOf(args []any) ([]*ir.RoleSeed, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("@seedRoles takes one list of roles: @seedRoles([{ name: \"member\", permissions: [\"orders.read\"] }])")
	}
	list, ok := args[0].([]any)
	if !ok || len(list) == 0 {
		return nil, ArgErrorf(0, "@seedRoles takes a list of one or more roles, each { name, permissions }")
	}
	seeds := make([]*ir.RoleSeed, 0, len(list))
	for i, item := range list {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, ArgErrorf(0, "@seedRoles role %d is %v; write { name, permissions }", i, item)
		}
		seed := &ir.RoleSeed{}
		var hasPermissions bool
		for key, value := range obj {
			switch key {
			case "name":
				s, ok := value.(string)
				if !ok {
					return nil, ArgErrorf(0, "@seedRoles role %d's name is %v; it is a string", i, value)
				}
				seed.Name = s
			case "permissions":
				permissions, err := stringList(value)
				if err != nil {
					return nil, ArgErrorf(0, "@seedRoles role %d's permissions: %v", i, err)
				}
				seed.Permissions, hasPermissions = permissions, true
			default:
				return nil, ArgErrorf(0, "@seedRoles role %d has no key %q; a role takes name and permissions", i, key)
			}
		}
		if seed.Name == "" || !hasPermissions {
			return nil, ArgErrorf(0, "@seedRoles role %d needs a name and permissions, a list that may be empty", i)
		}
		seeds = append(seeds, seed)
	}
	return seeds, nil
}
