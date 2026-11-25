package typegen

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/goutil"
	ir "github.com/parable-work/superschematic/ir"
)

// LimitConst is the Go constant of one declared field limit, such as
// OrderNotesMaxLength = 2000 (D160). It is untyped, so it compares with an
// int length and a float64 value alike.
type LimitConst struct {
	Name    string
	Kind    codegen.LimitKind
	Literal string
}

// fieldLimitConsts returns the constants of the limits a field declares
// with Validate<>, at their effective values (codegen.EffectiveLimits).
func fieldLimitConsts(typeName, fieldGoName string, limits []codegen.FieldLimit) []LimitConst {
	var consts []LimitConst
	for _, limit := range limits {
		if !limit.Declared {
			continue
		}
		consts = append(consts, LimitConst{
			Name:    goutil.LimitConstName(typeName, fieldGoName, limit.Kind),
			Kind:    limit.Kind,
			Literal: limit.Literal(),
		})
	}
	return consts
}

// setLimitConsts gives each field of types its limit constants. codegenTypes
// are the types convertTypes converted, in the same order.
func setLimitConsts(types []TypeInfo, codegenTypes []codegen.TypeInfo) {
	for i := range types {
		for j := range types[i].Fields {
			types[i].Fields[j].LimitConsts = fieldLimitConsts(types[i].Name, types[i].Fields[j].GoName, codegenTypes[i].Fields[j].Limits)
		}
	}
}

// importedLimitConsts returns the names of a dependency type's limit
// constants, so the importing module aliases them beside the type. Only a
// type the dependency writes as a struct has them.
func importedLimitConsts(typeDef *ir.TypeDef) []string {
	emitted := typeDef.Role == ir.RoleAPIInput
	for _, role := range localObjectRoles {
		emitted = emitted || typeDef.Role == role
	}
	if !emitted {
		return nil
	}
	var names []string
	for _, field := range typeDef.Fields {
		for _, kind := range codegen.DeclaredLimitKinds(field) {
			names = append(names, goutil.LimitConstName(typeDef.Name, codegen.ToPascalCase(field.Name), kind))
		}
	}
	return names
}

// HasLimitConsts reports whether any field of the type has a limit
// constant.
func (t TypeInfo) HasLimitConsts() bool {
	for _, field := range t.Fields {
		if len(field.LimitConsts) > 0 {
			return true
		}
	}
	return false
}

// checkLimitConstNames refuses a module whose limit constants collide with
// each other or with another Go name of the module. A type name that ends
// in a field name can give the name of a shorter type's constant, and Go
// refuses the duplicate declaration.
func checkLimitConstNames(output *ModuleOutput) error {
	taken := map[string]string{}
	for _, typeInfo := range output.Types {
		taken[typeInfo.Name] = "type " + typeInfo.Name
	}
	for _, imported := range output.ImportedTypes {
		taken[imported.Name] = "type " + imported.Name
	}
	for _, union := range output.Unions {
		taken[union.Name] = "union " + union.Name
	}
	for _, imported := range output.ImportedUnions {
		taken[imported.Name] = "union " + imported.Name
	}
	for _, enum := range output.Enums {
		taken[enum.Name] = "enum " + enum.Name
	}
	for _, imported := range output.ImportedEnums {
		taken[imported.Name] = "enum " + imported.Name
	}
	claim := func(name, owner string) error {
		if previous, ok := taken[name]; ok {
			return fmt.Errorf("typegen: limit constant %s of %s collides with %s; rename one of them", name, owner, previous)
		}
		taken[name] = "a limit of " + owner
		return nil
	}
	for _, typeInfo := range output.Types {
		for _, field := range typeInfo.Fields {
			for _, limitConst := range field.LimitConsts {
				if err := claim(limitConst.Name, typeInfo.Name+"."+field.Name); err != nil {
					return err
				}
			}
		}
	}
	for _, imported := range output.ImportedTypes {
		for _, name := range imported.LimitConsts {
			if err := claim(name, imported.Name); err != nil {
				return err
			}
		}
	}
	return nil
}
