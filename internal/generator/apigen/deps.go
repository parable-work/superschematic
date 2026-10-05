package apigen

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// DepsInfo is what the generated Deps holds beside the config and the
// logger (docs/stack-model.md, section 8.5): the ORM of the API's database
// and a Go SDK client per API it calls.
type DepsInfo struct {
	// Database is the DB service whose ORM Deps.DB is: the API's authDb,
	// or its one DB-kind dependency. Empty when the API has neither.
	Database string

	// ORMModule is Database's Go ORM module.
	ORMModule string

	// VersionGraph reports whether Database declares a version graph: its
	// ORM then imports the version-graph core's Go binding, which go.mod
	// replaces.
	VersionGraph bool

	// Calls are the API's calls entries, in order.
	Calls []DepsCall
}

// DepsCall is a Go SDK client in Deps.
type DepsCall struct {
	// Service is the called API service.
	Service string

	// Field is the Deps field that holds the client.
	Field string

	// Module is the service's Go SDK module, imported as Alias.
	Module string
	Alias  string

	// Client is the SDK's root client type, which its New returns.
	Client string
}

// depsFields are the fields every Deps may hold beside its clients.
var depsFields = []string{"Config", "DB", "Logger"}

// SetDeps sets what Deps holds, refusing a client whose field would take
// the name of another of its fields.
func (o *APIOutput) SetDeps(deps DepsInfo) error {
	seen := map[string]string{}
	for _, name := range depsFields {
		seen[name] = "Deps." + name
	}
	for _, call := range deps.Calls {
		if prev, clash := seen[call.Field]; clash {
			return fmt.Errorf("apigen: %s calls %s, whose client would be Deps.%s, the field of %s", o.SchemaName, call.Service, call.Field, prev)
		}
		seen[call.Field] = call.Service
	}
	o.Deps = deps
	return nil
}

// DepsRequiresORM reports whether go.mod requires and replaces the ORM of
// the Deps database itself: a public API already does for its upstream
// auth schema's.
func (o *APIOutput) DepsRequiresORM() bool {
	return o.Deps.ORMModule != "" && !(o.IsPublic && o.Deps.ORMModule == o.ORMModule)
}

// HasEnvConfig reports whether config.go declares EnvConfig, which
// Deps.Config holds.
func (o *APIOutput) HasEnvConfig() bool {
	return o.EnvConfig != nil && o.EnvConfig.EnvConfig
}

// ImplementationFile is the file the implementation scaffold writes.
const ImplementationFile = "implementation.go"

// WriteImplementationScaffold writes the scaffold of the API's
// implementation into dir, the package the naming file's
// [implementation_paths] go template places it in, when the package is
// missing: when dir holds no .go file. It never writes into a package that
// exists, so it never overwrites the engineer's code, and reports whether
// it wrote. The scaffold's New has the generated Constructor's signature
// and each method returns a not-implemented error (docs/stack-model.md,
// section 8.5).
func WriteImplementationScaffold(output *APIOutput, dir string) (bool, error) {
	exists, err := ImplementationExists(dir)
	if err != nil || exists {
		return false, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("create implementation package %s: %w", dir, err)
	}
	funcs, err := templateFuncs(output.Provider)
	if err != nil {
		return false, err
	}
	generator := codegen.NewFileGenerator(templatesFS, funcs)
	if err := generateFile(generator, templatesFS, "implementation.tmpl", filepath.Join(dir, ImplementationFile), output, funcs); err != nil {
		return false, fmt.Errorf("write implementation scaffold: %w", err)
	}
	return true, nil
}

// ImplementationExists reports whether dir holds a Go package: a .go file.
func ImplementationExists(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read implementation package %s: %w", dir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			return true, nil
		}
	}
	return false, nil
}
