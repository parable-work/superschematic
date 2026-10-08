package tsrestgen

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The implementation side of a TypeScript API (D51; docs/stack-model.md,
// sections 8.5 and 8.6): deps.ts declares Deps, what the implementation is
// built from, and the types of the implementation's create and
// authenticate, the twin of the Go API's deps.go; the scaffold writes the
// implementation's package once, at the naming file's
// [implementation_paths] typescript template.

const (
	// PGPeerRange is the pg the package peers on when Deps holds a pool:
	// the HTTP runtime's ./postgres entry's range.
	PGPeerRange = ">=8.11.0"
	// PGTypesVersion pins the @types/pg deps.ts type-checks against.
	PGTypesVersion = "8.23.1"
	// NodeTypesVersion pins the @types/node the generated package and the
	// scaffold type-check against.
	NodeTypesVersion = "22.19.9"
)

// DepsInfo is what Deps holds beside the config and the logger: a pg Pool
// for the API's database, and an SDK client per API it calls.
type DepsInfo struct {
	// Database is the DB service whose pool Deps.db is: the API's authDb,
	// or its one DB-kind dependency. Empty when the API has neither.
	Database string

	// Calls are the API's calls entries, in order.
	Calls []DepsCall
}

// DepsCall is an SDK client in Deps.
type DepsCall struct {
	// Service is the called API service.
	Service string

	// Field is the Deps member that holds the client.
	Field string

	// Package is the service's TypeScript SDK package, which exports
	// Client.
	Package string

	// Client is the SDK's root client class.
	Client string
}

// depsFields are the members every Deps may hold beside its clients.
var depsFields = []string{"config", "db", "logger"}

// SetDeps sets what Deps holds, refusing a client whose member would take
// the name of another of its members.
func (o *APIOutput) SetDeps(deps DepsInfo) error {
	seen := map[string]string{}
	for _, name := range depsFields {
		seen[name] = "Deps." + name
	}
	for _, call := range deps.Calls {
		if prev, clash := seen[call.Field]; clash {
			return fmt.Errorf("tsrestgen: %s calls %s, whose client would be Deps.%s, the member of %s", o.SchemaName, call.Service, call.Field, prev)
		}
		seen[call.Field] = call.Service
	}
	o.Deps = deps
	return nil
}

// ChecksEndUsers reports whether an operation requires an authenticated
// end user, which the router establishes with RouterOptions.authenticate:
// the implementation then exports authenticate, an AuthenticatorFactory.
func (o *APIOutput) ChecksEndUsers() bool {
	for _, ep := range o.Endpoints {
		if ep.RequiresAuth && !ep.PublicRoute {
			return true
		}
	}
	return false
}

// DepsPeerPackages lists the packages deps.ts imports beside the runtime:
// each callee's SDK, in order.
func (o *APIOutput) DepsPeerPackages() []string {
	var out []string
	for _, call := range o.Deps.Calls {
		out = append(out, call.Package)
	}
	return out
}

// ImplementationFile is the file the scaffold writes the implementation
// in; the scaffold also writes package.json and tsconfig.json.
const ImplementationFile = "index.ts"

// WriteImplementationScaffold writes the scaffold of the API's
// TypeScript implementation into dir, the package the naming file's
// [implementation_paths] typescript template places it in, when the
// package is missing: when dir holds no .ts file. It never writes into a
// package that exists, so it never overwrites the engineer's code, and
// reports whether it wrote. The scaffold's create has the generated
// Constructor's type and each method throws the runtime's not-implemented
// problem, which answers 501.
func WriteImplementationScaffold(output *APIOutput, dir string) (bool, error) {
	exists, err := ImplementationExists(dir)
	if err != nil || exists {
		return false, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("create implementation package %s: %w", dir, err)
	}
	for _, file := range []struct{ template, name string }{
		{"implementation-package.tmpl", "package.json"},
		{"implementation-tsconfig.tmpl", "tsconfig.json"},
		{"implementation.tmpl", ImplementationFile},
	} {
		path := filepath.Join(dir, file.name)
		if _, err := os.Stat(path); err == nil {
			// A package.json or tsconfig.json the engineer wrote stays.
			continue
		}
		if err := generateFile(file.template, path, output); err != nil {
			return false, fmt.Errorf("write implementation scaffold: %w", err)
		}
	}
	return true, nil
}

// ImplementationExists reports whether dir holds a TypeScript package: a
// .ts file.
func ImplementationExists(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read implementation package %s: %w", dir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".ts") {
			return true, nil
		}
	}
	return false, nil
}
