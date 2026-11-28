package cli

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/buildplan"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/schemadeps"
)

// graphProviders reads the runtime provider packages each service's
// config lists (D159), by its directory or its npm name (#608), for the
// dependency graph: the npm package, Go module and Rust crate each one's
// directory holds, which the API package of that language links.
func graphProviders(services []buildplan.Service) ([]schemadeps.Provider, error) {
	var out []schemadeps.Provider
	for _, service := range services {
		if service.Config == nil {
			continue
		}
		for _, entry := range service.Config.Providers {
			p, err := loader.ReadListedProvider(service.Dir, entry)
			if err != nil {
				return nil, fmt.Errorf("%s: providers: %s: %w", service.Name, entry, err)
			}
			out = append(out, schemadeps.Provider{Service: service.Name, Name: p.Name, Dir: p.Dir, Package: p.Package, Module: p.Module, Crate: p.Crate})
		}
	}
	return out, nil
}
