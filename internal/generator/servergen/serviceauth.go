package servergen

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// ServiceAuthFile is the file of an entrypoint module that builds the
// service authenticator of each served API with a service clause (D37;
// docs/stack-model.md, sections 8.1 and 9.2): serviceAuthenticator, which
// main.go calls once per such API, reads the API's callers field with the
// HTTP runtime's stackconfig.LoadCallers and returns a serviceauth.Verifier
// over it. A server whose APIs have no service clause has no such file.
//
// Its one package-level name, callersFields, takes no name Plan hands out:
// the import names are lower case, and the variables of main.go are local
// to run.
const ServiceAuthFile = "serviceauth.go"

// CallersField is the API's callers field, which its service
// authenticator verifies a caller's credential against: the name the
// resolver binds it under (ir.CallersField).
func (a *API) CallersField() string {
	return ir.CallersField(a.Service)
}

// writeServiceAuth writes serviceauth.go into dir when an API of s has a
// service clause.
func writeServiceAuth(s *Server, dir string) error {
	if !slices.ContainsFunc(s.APIs, func(a *API) bool { return a.ServiceAuth }) {
		return nil
	}
	gen := codegen.NewFileGenerator(templatesFS, templateFuncs())
	if err := gen.GenerateFile(codegen.NewGoFileConfig(templatesFS, "serviceauth.go.tmpl", filepath.Join(dir, ServiceAuthFile), s, nil)); err != nil {
		return fmt.Errorf("servergen: server %s: %w", s.Name, err)
	}
	return nil
}
