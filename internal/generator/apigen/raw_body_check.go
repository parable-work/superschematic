package apigen

import (
	"fmt"
	"go/token"
	"sort"
	"strings"
)

// RawBodyCheck names a Go function a generated route calls on the raw JSON
// of its request body before it decodes the body. A distribution registers
// one for a scalar (registry.ScalarCatalogWithRawBodyChecks) when decoding
// a value of that scalar loses something a write must refuse: a key the
// decoded form has no place for, a repeated key, a spelling it normalizes
// away. A route whose input type has single-valued top-level fields of the
// scalar calls
//
//	PackageName.Func(body, "field", ...)
//
// with the raw body and the wire names of those fields, in field order,
// and answers 400 with the result when its HasErrors reports true. The
// function's signature is func([]byte, ...string) E, where E has a
// HasErrors() bool method and is assignable to the generated types
// module's ValidationErrors, which RespondValidationErrors takes. It
// reports each error at its path under the field it names.
//
// The route calls it in each place it decodes an input type from JSON: the
// JSON body of a route without file uploads, and the JSON body or the
// multipart "data" part of a route with them. It runs after the route has
// read the body and refused a null one, and before json.Unmarshal.
type RawBodyCheck struct {
	// ImportPath is the Go import path of the package that declares Func.
	ImportPath string
	// PackageName is the name routes.go imports ImportPath under and
	// qualifies Func with.
	PackageName string
	// Func is the exported name of the function.
	Func string
	// ErrorsVar names the variable the call's result is bound to; empty
	// means checkErrors. It lets a distribution keep the output of a
	// generator it ported byte for byte.
	ErrorsVar string
	// Comment is written above the call as line comments, one per line of
	// it, in the two JSON-body paths; the multipart "data" part's call has
	// none. Empty writes no comment.
	Comment string
}

// RawBodyChecks looks up the raw-body check registered for a scalar by its
// canonical name. The registry's RawBodyChecks supplies it from the
// registered scalar catalog.
type RawBodyChecks interface {
	RawBodyCheck(scalar string) (RawBodyCheck, bool)
}

// RawBodyCheckCall is one raw-body check a route runs, with the wire names
// of the input fields it checks, in field order.
type RawBodyCheckCall struct {
	RawBodyCheck
	Fields []string
}

// RawBodyCheckImport is one package routes.go imports for raw-body checks.
type RawBodyCheckImport struct {
	Name string
	Path string
}

// defaultRawBodyCheckErrorsVar is the result variable of a check without
// ErrorsVar.
const defaultRawBodyCheckErrorsVar = "checkErrors"

// Var is the name of the variable the call's result is bound to.
func (c RawBodyCheck) Var() string {
	if c.ErrorsVar == "" {
		return defaultRawBodyCheckErrorsVar
	}
	return c.ErrorsVar
}

// routesImportNames are the names routes.go imports its own packages
// under. A check's package may not take one of them.
var routesImportNames = map[string]bool{
	"json": true, "fmt": true, "gohttp": true, "regexp": true, "strconv": true, "strings": true, "time": true,
	"chi": true, "filterparse": true, "bodyargs": true, "runtimemiddleware": true, "runtimerouting": true,
	"orm": true, "types": true, "zap": true,
}

// rawBodyCheckCallReads are the names the generated call reads besides its
// arguments' package; a result variable of the same name would shadow one.
var rawBodyCheckCallReads = map[string]bool{"w": true, "r": true, "rawInput": true, "dataField": true}

// Validate reports what keeps c from rendering a call that compiles: an
// import path that cannot be quoted as one, a package name that is not an
// identifier or is one routes.go imports its own packages under, an
// unexported function, a result variable the call reads, and a carriage
// return in the comment.
func (c RawBodyCheck) Validate() error {
	if c.ImportPath == "" || strings.ContainsAny(c.ImportPath, "\"\\` \t\r\n") {
		return fmt.Errorf("raw-body check %s: import path %q is not a Go import path", c.Func, c.ImportPath)
	}
	if !token.IsIdentifier(c.PackageName) || c.PackageName == "_" {
		return fmt.Errorf("raw-body check %s: package name %q is not a Go identifier", c.Func, c.PackageName)
	}
	if routesImportNames[c.PackageName] {
		return fmt.Errorf("raw-body check %s: package name %q is one routes.go already imports a package under", c.Func, c.PackageName)
	}
	if !token.IsIdentifier(c.Func) || !token.IsExported(c.Func) {
		return fmt.Errorf("raw-body check %q: the function must be an exported Go identifier", c.Func)
	}
	if c.ErrorsVar != "" && (!token.IsIdentifier(c.ErrorsVar) || c.ErrorsVar == "_" || rawBodyCheckCallReads[c.ErrorsVar]) {
		return fmt.Errorf("raw-body check %s: result variable %q must be a Go identifier the call does not read", c.Func, c.ErrorsVar)
	}
	if strings.Contains(c.Comment, "\r") {
		return fmt.Errorf("raw-body check %s: the comment holds a carriage return; separate its lines with \\n", c.Func)
	}
	return nil
}

// rawBodyCheckCalls returns the checks a route runs on an input with
// fields: one call per distinct check that a single-valued field's type
// has, naming each such field, ordered by each check's first field. A
// list or a map of the scalar is not named: the check reads one value per
// field.
func rawBodyCheckCalls(fields []Param, checks RawBodyChecks) []RawBodyCheckCall {
	if checks == nil {
		return nil
	}
	var calls []RawBodyCheckCall
	for _, field := range fields {
		if field.IsArray || field.IsMap {
			continue
		}
		check, ok := checks.RawBodyCheck(field.Type)
		if !ok {
			continue
		}
		index := -1
		for i := range calls {
			if calls[i].RawBodyCheck == check {
				index = i
				break
			}
		}
		if index < 0 {
			calls = append(calls, RawBodyCheckCall{RawBodyCheck: check})
			index = len(calls) - 1
		}
		calls[index].Fields = append(calls[index].Fields, field.Name)
	}
	return calls
}

// rawBodyCheckImports validates the checks the endpoints run and returns
// the packages routes.go imports for them, one per package name, sorted by
// path. Two checks may not import different packages under one name.
func rawBodyCheckImports(endpoints []EndpointInfo) ([]RawBodyCheckImport, error) {
	pathByName := map[string]string{}
	var imports []RawBodyCheckImport
	for _, endpoint := range endpoints {
		for _, call := range endpoint.RawBodyChecks {
			if err := call.Validate(); err != nil {
				return nil, fmt.Errorf("apigen: operation %s.%s: %w", endpoint.Namespace, endpoint.Name, err)
			}
			path, seen := pathByName[call.PackageName]
			if seen && path != call.ImportPath {
				return nil, fmt.Errorf("apigen: raw-body checks import %s and %s under one package name, %s", path, call.ImportPath, call.PackageName)
			}
			if !seen {
				pathByName[call.PackageName] = call.ImportPath
				imports = append(imports, RawBodyCheckImport{Name: call.PackageName, Path: call.ImportPath})
			}
		}
	}
	sort.Slice(imports, func(i, j int) bool {
		if imports[i].Path != imports[j].Path {
			return imports[i].Path < imports[j].Path
		}
		return imports[i].Name < imports[j].Name
	})
	return imports, nil
}

// ImportsRawBodyCheckPackage reports whether routes.go imports importPath
// for a raw-body check. An auth provider's routesImports snippet that needs
// the same package leaves its own import of it out.
func (o *APIOutput) ImportsRawBodyCheckPackage(importPath string) bool {
	for _, imported := range o.RawBodyCheckImports {
		if imported.Path == importPath {
			return true
		}
	}
	return false
}

// commentLines renders comment as Go line comments, one per line of it.
func commentLines(comment string) []string {
	if comment == "" {
		return nil
	}
	lines := strings.Split(comment, "\n")
	for i, line := range lines {
		if line == "" {
			lines[i] = "//"
			continue
		}
		lines[i] = "// " + line
	}
	return lines
}
