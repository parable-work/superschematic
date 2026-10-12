package servergen

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	ir "github.com/parable-work/superschematic/ir"
)

// CORSFile is the file of an entrypoint module that builds the CORS policy
// of each served API a site of the stack calls (D55; docs/stack-model.md,
// section 8.10): corsPolicy, which main.go calls once per such API, reads
// the API's CORS field with the HTTP runtime's stackconfig.LoadCORS, and
// corsRoutes matches a request to the API whose router takes it, as
// main.go's dispatch does. A server no site calls has no such file.
//
// Its package-level names, corsAPIs, corsPolicy and corsRoutes, take no
// name Plan hands out: the import names are lower case words with no
// capital, and the variables of main.go are local to run.
const CORSFile = "cors.go"

// CORSField is the API's CORS field, the origins of the sites that call
// it: the name the resolver binds it under (ir.CORSField).
func (a *API) CORSField() string {
	return ir.CORSField(a.Service)
}

// endpointMethods returns the HTTP methods of an API's endpoints, sorted,
// each once: what a CORS preflight's answer allows.
func endpointMethods(o *apigen.APIOutput) []string {
	var out []string
	for _, ep := range o.Endpoints {
		if !slices.Contains(out, ep.Method) {
			out = append(out, ep.Method)
		}
	}
	slices.Sort(out)
	return out
}

// writeCORS writes cors.go into dir when a site calls an API of s. A
// job's never answers CORS: it serves no request.
func writeCORS(s *Server, dir string) error {
	if !slices.ContainsFunc(s.APIs, func(a *API) bool { return a.CORS }) {
		return nil
	}
	if err := render("cors.go.tmpl", filepath.Join(dir, CORSFile), s); err != nil {
		return fmt.Errorf("servergen: server %s: %w", s.Name, err)
	}
	return nil
}
