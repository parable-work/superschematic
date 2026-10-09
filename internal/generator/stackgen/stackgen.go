// Package stackgen is the Stack kind's generator (docs/stack-model.md,
// sections 4 and 5): it reads the stack a Stack schema declares, loads every
// service the stack reaches, resolves each environment and writes it to
// `<output-root>/stack/<stack>/<environment>/environment.json`. A stack that
// does not resolve fails the build with every problem resolution found,
// which is level 1 of section 10.
package stackgen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
)

// Name is the generator's name in the Stack kind's pipeline.
const Name = "stack"

// OutDir is where the environments of the stack the service declares are
// written: a stack takes its service's name.
func OutDir(outputRoot, service string) string {
	return filepath.Join(outputRoot, "stack", service)
}

// Generate resolves every environment of the stack c.Schema declares and
// writes each one's environment.json under OutDir, which it empties first so
// a removed environment leaves no file behind. Nothing is written unless
// every environment resolves.
func Generate(c registry.GenerateContext) error {
	st := ir.StackOf(c.Schema)
	if st == nil {
		return fmt.Errorf("stack %s: no class declares @stack", c.Config.Name)
	}
	services, err := Services(c, st)
	if err != nil {
		return err
	}
	resolved, err := Resolve(c, st, services)
	if err != nil {
		return err
	}
	dir := OutDir(c.Options.OutputRoot, st.Name)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("stack %s: %w", st.Name, err)
	}
	for _, env := range resolved {
		if _, err := stack.Write(c.Options.OutputRoot, env); err != nil {
			return err
		}
	}
	if len(resolved) == 0 {
		c.Skip(Name)
		return nil
	}
	c.Done(Name, dir)
	return nil
}

// Resolve resolves every environment of st over services, in declaration
// order. When any fails to resolve it returns none of them, and every
// failing environment's problems together.
func Resolve(c registry.GenerateContext, st *ir.Stack, services []stack.Service) ([]*ir.ResolvedEnvironment, error) {
	var resolved []*ir.ResolvedEnvironment
	var failures []error
	for _, env := range st.Environments {
		out, err := stack.Resolve(c.Registry, stack.Input{Stack: st, Services: services, Environment: env.Name})
		if err != nil {
			failures = append(failures, err)
			continue
		}
		resolved = append(resolved, out)
	}
	if len(failures) > 0 {
		return nil, errors.Join(failures...)
	}
	return resolved, nil
}

// Services loads the facts of every service st reaches, as resolution
// collects them: its entry points and the services its declared
// deployables serve and host, and from each API service its authDb, its
// DB dependencies and its calls. Each is loaded with c.LoadDependency, its
// outputs come from c.Options.LoadDependencyConfig, and Service reads the
// facts from the two. A handle to a service of the wrong kind is loaded
// all the same, so resolution reports the mismatch.
func Services(c registry.GenerateContext, st *ir.Stack) ([]stack.Service, error) {
	if c.Options.LoadDependencyConfig == nil {
		return nil, fmt.Errorf("stack %s: the build reads no service configs (Options.LoadDependencyConfig)", st.Name)
	}
	seen := map[string]bool{}
	var queue []string
	reach := func(refs ...ir.ServiceRef) {
		for _, ref := range refs {
			if ref.Name != "" && !seen[ref.Name] {
				seen[ref.Name] = true
				queue = append(queue, ref.Name)
			}
		}
	}
	reach(st.Deploy...)
	for _, decl := range st.Deployables {
		if decl != nil {
			reach(decl.Serves...)
			reach(decl.Hosts...)
		}
	}
	var services []stack.Service
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		svc, err := loadService(c, name)
		if err != nil {
			return nil, fmt.Errorf("stack %s: service %s: %w", st.Name, name, err)
		}
		services = append(services, svc)
		if svc.Kind != ir.SchemaKindAPI {
			continue
		}
		if svc.AuthDB != nil {
			reach(*svc.AuthDB)
		}
		for _, dep := range svc.Dependencies {
			if dep.Kind == ir.SchemaKindDB {
				reach(dep)
			}
		}
		reach(svc.Calls...)
	}
	return services, nil
}

// loadService loads one service's IR, config and imported schemas and reads
// its facts.
func loadService(c registry.GenerateContext, name string) (stack.Service, error) {
	schema, err := c.LoadDependency(name)
	if err != nil {
		return stack.Service{}, err
	}
	cfg, err := c.Options.LoadDependencyConfig(name)
	if err != nil {
		return stack.Service{}, err
	}
	outputs, err := registry.ParseOutputs(cfg.Outputs, c.Registry)
	if err != nil {
		return stack.Service{}, fmt.Errorf("schema config: %w", err)
	}
	var imported map[string]*ir.Schema
	if schema.Kind == ir.SchemaKindAPI {
		for _, imp := range schema.Imports {
			dep := typegen.DependencyServiceName(imp.Package)
			if imported[dep] != nil {
				continue
			}
			depSchema, err := c.LoadDependency(dep)
			if err != nil {
				return stack.Service{}, fmt.Errorf("load %s, whose types it imports: %w", dep, err)
			}
			if imported == nil {
				imported = map[string]*ir.Schema{}
			}
			imported[dep] = depSchema
		}
	}
	svc, err := Service(schema, outputs, imported)
	if err != nil {
		return stack.Service{}, err
	}
	if schema.Kind == ir.SchemaKindAPI {
		if svc.Identity, err = declaresUserModel(c, schema, cfg.Public); err != nil {
			return stack.Service{}, err
		}
	}
	return svc, nil
}

// declaresUserModel reports whether the server of the API schema
// authenticates with the identity runtime (D50): whether the database its
// Go server's auth reads, its authDb or, for a public API, its one DB-kind
// dependency, has a User table, as the api generator reads it.
func declaresUserModel(c registry.GenerateContext, schema *ir.Schema, public bool) (bool, error) {
	name := schema.AuthDB
	if name == "" && public {
		for _, dep := range schema.Dependencies {
			if dep.Kind != ir.SchemaKindDB {
				continue
			}
			if name != "" {
				return false, nil
			}
			name = dep.Name
		}
	}
	if name == "" {
		return false, nil
	}
	db, err := c.LoadDependency(name)
	if err != nil {
		return false, fmt.Errorf("load %s, the auth database of %s: %w", name, schema.Name, err)
	}
	return db.UserTable() != nil, nil
}
