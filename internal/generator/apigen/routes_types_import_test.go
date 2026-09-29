package apigen_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	ir "github.com/parable-work/superschematic/ir"
)

// TestRoutesImportTypesExactlyWhenUsed renders routes.go for one-operation
// schemas over body-args-api's scalars, enum and object type: each way a
// handler takes an argument (path, @query, query string of a GET operation,
// body), each argument type, required or not, a list or not, under each
// validation rule, an input type, and responses of builtin and generated
// types, alone, in a list and in a list of lists. routes.go must import the
// generated types module exactly when it references it; a routes.tmpl
// branch that starts or stops using types without RoutesNeedTypes
// following fails here.
func TestRoutesImportTypesExactlyWhenUsed(t *testing.T) {
	base := loadBodyArgsAPI(t)
	one := 1.0
	length := 1
	rules := []struct {
		name  string
		apply func(*ir.ArgumentDef)
	}{
		{"none", func(*ir.ArgumentDef) {}},
		{"min", func(a *ir.ArgumentDef) { a.ValidateMin = &one }},
		{"max", func(a *ir.ArgumentDef) { a.ValidateMax = &one }},
		{"minLength", func(a *ir.ArgumentDef) { a.ValidateMinLength = &length }},
		{"maxLength", func(a *ir.ArgumentDef) { a.ValidateMaxLength = &length }},
		{"pattern", func(a *ir.ArgumentDef) { a.ValidatePattern = "^[a-z]+$" }},
		{"listMin", func(a *ir.ArgumentDef) { a.ValidateListMin = &length }},
	}
	placements := []struct {
		name, method, restPath string
		query, lists           bool
	}{
		{"path", "GET", "probe/{value}", false, false},
		{"query", "GET", "probe", true, true},
		{"getArg", "GET", "probe", false, true},
		{"bodyArg", "POST", "probe", false, true},
	}
	argTypes := []string{"string", "number", "boolean", "Network.Url", "Ordering.Rank", "Identity.UUID", "Temporal.DateTime", "Shade"}

	type probe struct {
		name string
		op   *ir.FieldDef
	}
	var probes []probe
	for _, placement := range placements {
		for _, typeName := range argTypes {
			for _, isArray := range []bool{false, true} {
				if isArray && !placement.lists {
					continue
				}
				for _, required := range []bool{false, true} {
					for _, rule := range rules {
						arg := &ir.ArgumentDef{
							Name:     "value",
							TypeRef:  ir.TypeRef{Name: typeName, IsArray: isArray},
							Required: required,
							IsQuery:  placement.query,
						}
						rule.apply(arg)
						probes = append(probes, probe{
							name: placement.name + "/" + typeName + "/array=" + strconv.FormatBool(isArray) + "/required=" + strconv.FormatBool(required) + "/" + rule.name,
							op: &ir.FieldDef{
								Name:       "probe",
								TypeRef:    ir.TypeRef{Name: "boolean"},
								Required:   true,
								HTTPMethod: placement.method,
								RestPath:   placement.restPath,
								Arguments:  []*ir.ArgumentDef{arg},
							},
						})
					}
				}
			}
		}
	}
	probes = append(probes, probe{
		name: "input",
		op: &ir.FieldDef{
			Name: "probe", TypeRef: ir.TypeRef{Name: "boolean"}, Required: true, HTTPMethod: "POST", RestPath: "probe",
			Arguments: []*ir.ArgumentDef{{Name: "point", TypeRef: ir.TypeRef{Name: "Point"}, Required: true}},
		},
	})
	for _, output := range []string{"boolean", "string", "Ordering.Rank", "Shade", "Point"} {
		for _, depth := range []int{0, 1, 2} {
			probes = append(probes, probe{
				name: "output/" + output + "/depth=" + strconv.Itoa(depth),
				op: &ir.FieldDef{
					Name: "probe", TypeRef: ir.TypeRef{Name: output, IsArray: depth > 0, IsArrayOfArrays: depth == 2},
					Required: true, HTTPMethod: "GET", RestPath: "probe",
				},
			})
		}
	}

	outDir := t.TempDir()
	for _, p := range probes {
		schema := *base
		set := "ProbeQueries"
		if p.op.HTTPMethod != "GET" {
			set = "ProbeMutations"
		}
		schema.OperationSets = []*ir.OperationSet{{Name: set, Operations: []*ir.FieldDef{p.op}}}
		output, err := apigen.Generate(&schema, apigen.Options{
			Provider:    sessionauth.Provider{},
			SchemaName:  bodyArgsAPI,
			ModulePath:  "example.com/schemas/api/" + bodyArgsAPI,
			TypesModule: "example.com/schemas/types/go/" + bodyArgsAPI,
			Clock:       goModuleClock,
		})
		if err != nil {
			t.Fatalf("%s: apigen.Generate: %v", p.name, err)
		}
		if err := apigen.WriteAPIWithProfile(output, outDir, nil, true); err != nil {
			t.Fatalf("%s: write api: %v", p.name, err)
		}
		imported, used := routesTypesImport(t, filepath.Join(outDir, "routes.go"), output.TypesModule)
		if imported != used {
			t.Errorf("%s: routes.go imports the types module: %v, references it: %v", p.name, imported, used)
		}
	}
}

// routesTypesImport reports whether the Go file at path imports
// typesModule as types and whether it references a types.<name>.
func routesTypesImport(t *testing.T, path, typesModule string) (imported, used bool) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, spec := range file.Imports {
		if importPath, _ := strconv.Unquote(spec.Path.Value); importPath == typesModule {
			imported = true
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		if selector, ok := node.(*ast.SelectorExpr); ok {
			if ident, ok := selector.X.(*ast.Ident); ok && ident.Name == "types" {
				used = true
			}
		}
		return !used
	})
	return imported, used
}
