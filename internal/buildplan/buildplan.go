// Package buildplan discovers schema services and computes their build order.
package buildplan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/loader/tsreader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sentinel"
	ir "github.com/parable-work/superschematic/ir"
)

var configNames = []string{"schema.config.ts", "schema.config.json", "schema.config.yaml"}

// Service describes one buildable schema service.
type Service struct {
	Name       string
	Dir        string
	ConfigPath string
	Config     *schemaconfig.SchemaConfig
	OutputDirs []string
}

// reference is one service a config names: the build-order edge from the
// config's service to it.
type reference struct {
	// name is the service named.
	name string

	// field is the config key that names it: dependencies, authDb or calls.
	field string

	// kind is the kind the handle gives the service. It is empty for an
	// authDb a data-form config names by name alone.
	kind ir.SchemaKind
}

// references lists every service the config names, in the order of its
// fields: the declared dependencies, the authDb, then the calls. A service
// named by two fields appears once for each.
func (s Service) references() []reference {
	refs := make([]reference, 0, len(s.Config.Dependencies)+1+len(s.Config.Calls))
	for _, dep := range s.Config.Dependencies {
		refs = append(refs, reference{name: dep.Name, field: "dependencies", kind: dep.Kind})
	}
	if s.Config.AuthDB != "" {
		refs = append(refs, reference{name: s.Config.AuthDB, field: "authDb", kind: s.Config.AuthDBKind})
	}
	for _, call := range s.Config.Calls {
		refs = append(refs, reference{name: call.Name, field: "calls", kind: call.Kind})
	}
	return refs
}

// dependencyNames lists the services that must be built before this one,
// each once: every service its config names. The API generator loads the
// authDb as the upstream auth schema and the generated API module imports
// its ORM and types packages, so the authDb is a build-order edge even when
// the config does not also declare it as a dependency. A callee in calls is
// one too: the code generated for the caller imports the callee's SDK
// (docs/stack-model.md, sections 3.3 and 8.5).
func (s Service) dependencyNames() []string {
	refs := s.references()
	names := make([]string, 0, len(refs))
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if seen[ref.name] {
			continue
		}
		seen[ref.name] = true
		names = append(names, ref.name)
	}
	return names
}

// Discover scans servicesRoot for schema services and returns them in
// dependency order, with the output surface of the core registry.
func Discover(servicesRoot string, outputRoot string) ([]Service, error) {
	return DiscoverWith(servicesRoot, outputRoot, nil)
}

// DiscoverWith is Discover against a registry: its documents and generators
// decide each service's OutputDirs. nil means the core registry.
func DiscoverWith(servicesRoot string, outputRoot string, reg *registry.Registry) ([]Service, error) {
	if reg == nil {
		reg = generator.CoreRegistry(naming.Default())
	}
	entries, err := os.ReadDir(servicesRoot)
	if err != nil {
		return nil, fmt.Errorf("reading services root %s: %w", servicesRoot, err)
	}

	var services []Service
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), "_") {
			continue
		}
		serviceDir := filepath.Join(servicesRoot, entry.Name())
		configPath := configPath(serviceDir)
		if configPath == "" {
			continue
		}
		cfg, err := readConfig(serviceDir, configPath, reg)
		if err != nil {
			return nil, fmt.Errorf("reading config for %s: %w", serviceDir, err)
		}
		outputDirs, err := generator.ExpectedOutputDirs(outputRoot, cfg, serviceDir, reg)
		if err != nil {
			return nil, err
		}
		services = append(services, Service{
			Name:       cfg.Name,
			Dir:        serviceDir,
			ConfigPath: configPath,
			Config:     cfg,
			OutputDirs: outputDirs,
		})
	}
	sort.SliceStable(services, func(i, j int) bool {
		return services[i].Dir < services[j].Dir
	})
	if err := validateDependencyKinds(services); err != nil {
		return nil, err
	}
	if err := validateHandleKinds(services); err != nil {
		return nil, err
	}
	return TopologicalSort(services)
}

// validateHandleKinds checks the kind each handle gives against the service
// it names: `authDb: service({ name: "shop-api", kind: SchemaKind.DB })` is
// refused when shop-api is an API. The loader checks a handle's kind only
// against the registry, and only discovery sees every service. A name that
// is not discovered is left to the steps that need the service:
// TopologicalSort tolerates it and Closure refuses it.
func validateHandleKinds(services []Service) error {
	byName := make(map[string]Service, len(services))
	for _, service := range services {
		byName[service.Name] = service
	}
	for _, service := range services {
		for _, ref := range service.references() {
			target, ok := byName[ref.name]
			if !ok || ref.kind == "" || ref.kind == target.Config.Kind {
				continue
			}
			return fmt.Errorf("%s: %s names %s with kind %s, but %s is kind %s", service.Name, ref.field, ref.name, ref.kind, ref.name, target.Config.Kind)
		}
	}
	return nil
}

// validateDependencyKinds rejects declared dependencies on schemas that emit
// no packages. Such a dependency can only be an
// authoring import in disguise -- source files imported by an executable
// deploy document -- and those are auto-tracked into the build cache, so the
// declaration is both unnecessary and a misuse of the codegen-dependency
// layer.
func validateDependencyKinds(services []Service) error {
	byName := make(map[string]Service, len(services))
	for _, service := range services {
		byName[service.Name] = service
	}
	for _, service := range services {
		for _, dep := range service.Config.Dependencies {
			depService, ok := byName[dep.Name]
			if !ok {
				continue
			}
			if len(depService.Config.Outputs) == 0 {
				return fmt.Errorf("%s: dependency %q emits no packages; authoring imports are auto-tracked, remove the dependency from schema.config", service.Name, dep.Name)
			}
		}
	}
	return nil
}

func configPath(serviceDir string) string {
	for _, name := range configNames {
		candidate := filepath.Join(serviceDir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func readConfig(serviceDir string, configPath string, reg *registry.Registry) (*schemaconfig.SchemaConfig, error) {
	if filepath.Base(configPath) == "schema.config.ts" {
		if err := checkConfigPurity(configPath, reg.Naming()); err != nil {
			return nil, err
		}
		return tsreader.ReadServiceConfig(serviceDir, reg)
	}
	return schemaconfig.ReadFile(serviceDir, reg)
}

// configImportPattern matches import declarations in a schema.config.ts:
// `import ... from "<specifier>"` and side-effect `import "<specifier>"`.
// schema.config.ts files are constrained enough (no strings containing
// import statements) that a line-level scan is reliable.
var configImportPattern = regexp.MustCompile(`(?m)^\s*import\b[^'"]*['"]([^'"]+)['"]`)

// checkConfigPurity enforces the identity layer's cycle-proofing rule:
// schema.config.ts may import only the config package
// (sentinel.ConfigPackage), under its own name or under a specifier the
// naming file's [package_aliases] maps onto it. Configs are imported as
// identity references by the platform model; any richer import graph would
// drag arbitrary code into every consumer's evaluation.
func checkConfigPurity(configPath string, n naming.Naming) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	for _, match := range configImportPattern.FindAllStringSubmatch(string(data), -1) {
		if n.DeclaringPackage(match[1]) != sentinel.ConfigPackage {
			return fmt.Errorf("%s: imports %q; schema.config.ts may import only %s (configs are identity references and must stay dependency-free)", configPath, match[1], strings.Join(configSpecifiers(n), " or "))
		}
	}
	return nil
}

// configSpecifiers returns the specifiers a schema.config.ts may import,
// sorted: the config package and every alias the naming maps onto it.
func configSpecifiers(n naming.Naming) []string {
	specifiers := []string{sentinel.ConfigPackage}
	for specifier, declaring := range n.PackageAliases {
		if declaring == sentinel.ConfigPackage && specifier != sentinel.ConfigPackage {
			specifiers = append(specifiers, specifier)
		}
	}
	sort.Strings(specifiers)
	return specifiers
}

// TopologicalSort returns services ordered so dependencies precede dependents.
func TopologicalSort(services []Service) ([]Service, error) {
	byName := make(map[string]Service, len(services))
	for _, service := range services {
		if _, ok := byName[service.Name]; ok {
			return nil, fmt.Errorf("duplicate schema service name %s", service.Name)
		}
		byName[service.Name] = service
	}

	visiting := make(map[string]bool, len(services))
	visited := make(map[string]bool, len(services))
	var result []Service
	var stack []string

	var visit func(Service) error
	visit = func(service Service) error {
		if visited[service.Name] {
			return nil
		}
		if visiting[service.Name] {
			return cycleError(byName, append(stack, service.Name))
		}
		visiting[service.Name] = true
		stack = append(stack, service.Name)
		for _, dep := range service.dependencyNames() {
			depService, ok := byName[dep]
			if !ok {
				continue
			}
			if err := visit(depService); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		visiting[service.Name] = false
		visited[service.Name] = true
		result = append(result, service)
		return nil
	}

	for _, service := range services {
		if err := visit(service); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// cycleError describes the cycle at the end of path, whose last service
// repeats an earlier one, edge by edge: "a calls b, b calls a". A cycle of
// calls alone gets a pointer to why it cannot build yet.
func cycleError(byName map[string]Service, path []string) error {
	last := path[len(path)-1]
	start := 0
	for i, name := range path[:len(path)-1] {
		if name == last {
			start = i
			break
		}
	}
	cycle := path[start:]
	edges := make([]string, 0, len(cycle)-1)
	callsOnly := true
	for i := 0; i+1 < len(cycle); i++ {
		from, to := cycle[i], cycle[i+1]
		var fields []string
		for _, ref := range byName[from].references() {
			if ref.name == to && !slices.Contains(fields, ref.field) {
				fields = append(fields, ref.field)
			}
		}
		if len(fields) != 1 || fields[0] != "calls" {
			callsOnly = false
		}
		verbs := make([]string, len(fields))
		for j, field := range fields {
			verbs[j] = edgeVerb(field)
		}
		edges = append(edges, fmt.Sprintf("%s %s %s", from, strings.Join(verbs, " and "), to))
	}
	msg := fmt.Sprintf("circular dependency involving %s: %s", last, strings.Join(edges, ", "))
	if callsOnly {
		msg += "; the build plan orders whole services, so APIs that call each other cannot build until it orders their outputs, each SDK before the APIs that call it (docs/stack-model.md, section 3.3)"
	}
	return errors.New(msg)
}

// edgeVerb phrases a config field as the relation it gives two services.
func edgeVerb(field string) string {
	switch field {
	case "calls":
		return "calls"
	case "authDb":
		return "authenticates against"
	default:
		return "depends on"
	}
}

// Closure returns root and every service it transitively depends on
// (declared dependencies, authDb and calls), in the order services already
// carries. It filters the Discover output rather than re-sorting it, so
// `build --with-deps` and build-all share one ordering. Where
// TopologicalSort tolerates a dependency on an undiscovered service so a
// partial tree can still be ordered, Closure fails on one: a member that
// is not there cannot be built.
func Closure(services []Service, root string) ([]Service, error) {
	byName := make(map[string]Service, len(services))
	for _, service := range services {
		byName[service.Name] = service
	}
	if _, ok := byName[root]; !ok {
		return nil, fmt.Errorf("schema service %s not found among the discovered services", root)
	}

	reachable := map[string]bool{root: true}
	pending := []string{root}
	for len(pending) > 0 {
		name := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		for _, dep := range byName[name].dependencyNames() {
			if reachable[dep] {
				continue
			}
			if _, ok := byName[dep]; !ok {
				return nil, fmt.Errorf("%s depends on %s, which is not a discovered schema service", name, dep)
			}
			reachable[dep] = true
			pending = append(pending, dep)
		}
	}

	closure := make([]Service, 0, len(reachable))
	for _, service := range services {
		if reachable[service.Name] {
			closure = append(closure, service)
		}
	}
	return closure, nil
}

// GroupIntoPhases groups services into dependency-safe parallel phases.
func GroupIntoPhases(services []Service, alreadyBuilt map[string]bool) ([][]Service, error) {
	built := make(map[string]bool, len(alreadyBuilt)+len(services))
	for name, ok := range alreadyBuilt {
		if ok {
			built[name] = true
		}
	}

	remaining := append([]Service(nil), services...)
	var phases [][]Service
	for len(remaining) > 0 {
		var phase []Service
		for _, service := range remaining {
			ready := true
			for _, dep := range service.dependencyNames() {
				if !built[dep] {
					ready = false
					break
				}
			}
			if ready {
				phase = append(phase, service)
			}
		}
		if len(phase) == 0 {
			names := make([]string, 0, len(remaining))
			for _, service := range remaining {
				names = append(names, service.Name)
			}
			sort.Strings(names)
			return nil, fmt.Errorf("circular dependency among %s", strings.Join(names, ", "))
		}
		phaseNames := make(map[string]bool, len(phase))
		for _, service := range phase {
			built[service.Name] = true
			phaseNames[service.Name] = true
		}
		next := remaining[:0]
		for _, service := range remaining {
			if !phaseNames[service.Name] {
				next = append(next, service)
			}
		}
		remaining = next
		phases = append(phases, phase)
	}
	return phases, nil
}
