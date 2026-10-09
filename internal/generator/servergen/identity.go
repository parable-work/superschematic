package servergen

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// IdentityFile is the file of an entrypoint module that reads the identity
// config of each served API over the user model (D50): identityConfig,
// which main.go calls once per such API with the API's identity config
// field, reads the field's JSON from the environment with the identity
// runtime's ParseConfig, and gives the runtime's defaults when the field
// is unset. A server whose APIs authenticate without the identity runtime
// has no such file.
const IdentityFile = "identity.go"

// IdentityField is the API's identity config field, which its identity
// config is read from: the name the resolver binds it under
// (ir.IdentityConfigField), in the style of its callers field.
func (a *API) IdentityField() string {
	return ir.IdentityConfigField(a.Service)
}

// writeIdentity writes identity.go into dir when an API of s authenticates
// with the identity runtime.
func writeIdentity(s *Server, dir string) error {
	if !slices.ContainsFunc(s.APIs, func(a *API) bool { return a.Identity }) {
		return nil
	}
	gen := codegen.NewFileGenerator(templatesFS, templateFuncs())
	if err := gen.GenerateFile(codegen.NewGoFileConfig(templatesFS, "identity.go.tmpl", filepath.Join(dir, IdentityFile), s, nil)); err != nil {
		return fmt.Errorf("servergen: server %s: %w", s.Name, err)
	}
	return nil
}
