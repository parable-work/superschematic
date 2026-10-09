// Package buildplan discovers schema services and computes their build order.
package buildplan

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
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

	// field is the config key that names it: dependencies, authDb, calls
	// or buckets.
	field string

	// kind is the kind the handle gives the service. It is empty for an
	// authDb a data-form config names by name alone.
	kind ir.SchemaKind
}

// references lists every service the config names, in the order of its
// fields: the declared dependencies, the authDb, the calls, then the
// buckets. A service named by two fields appears once for each.
func (s Service) references() []reference {
	refs := make([]reference, 0, len(s.Config.Dependencies))
	for _, dep := range s.Config.Dependencies {
		refs = append(refs, reference{name: dep.Name, field: "dependencies", kind: dep.Kind})
	}
	if s.Config.AuthDB != "" {
		refs = append(refs, reference{name: s.Config.AuthDB, field: "authDb", kind: s.Config.AuthDBKind})
	}
	for _, call := range s.Config.Calls {
		refs = append(refs, reference{name: call.Name, field: "calls", kind: call.Kind})
	}
	// A Bucket service builds nothing, so the reference orders no build
	// step (buildDependencyNames); it checks the handle's kind and keeps
	// the bucket in the closure `build --with-deps` builds (D54).
	for _, bucket := range s.Config.Buckets {
		refs = append(refs, reference{name: bucket.Name, field: "buckets", kind: bucket.Kind})
	}
	return refs
}

// dependencyNames lists every service the config names, each once: the
// closure `build --with-deps` builds. It is the build-order edges
// (buildDependencyNames) and the calls (callNames).
func (s Service) dependencyNames() []string {
	return s.namesOf(func(reference) bool { return true })
}

// buildDependencyNames lists the services whose outputs must be built
// before any of this one's: its declared dependencies and its authDb. The
// API generator loads the authDb as the upstream auth schema and the
// generated API module imports its ORM and types packages, so the authDb
// is a build-order edge even when the config does not also declare it as
// a dependency. A bucket is none: a Bucket service builds nothing (D54).
func (s Service) buildDependencyNames() []string {
	return s.namesOf(func(ref reference) bool { return ref.field != "calls" && ref.field != "buckets" })
}

// callNames lists the APIs the service calls. Only its API server reads
// them: the generated Deps imports each callee's SDK (docs/stack-model.md,
// sections 3.3 and 8.5), so a callee's SDK builds before the caller's
// server, and the rest of the caller's outputs need nothing of it.
func (s Service) callNames() []string {
	return s.namesOf(func(ref reference) bool { return ref.field == "calls" })
}

func (s Service) namesOf(keep func(reference) bool) []string {
	refs := s.references()
	names := make([]string, 0, len(refs))
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if seen[ref.name] || !keep(ref) {
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

// ReadConfig reads the config of the service in serviceDir in whichever form
// it has, as discovery reads every service's. A single build reads the
// configs of the services a stack reaches with it.
func ReadConfig(serviceDir string, reg *registry.Registry) (*schemaconfig.SchemaConfig, error) {
	path := configPath(serviceDir)
	if path == "" {
		return nil, fmt.Errorf("%s has no schema.config.ts, schema.config.json or schema.config.yaml", serviceDir)
	}
	return readConfig(serviceDir, path, reg)
}

func readConfig(serviceDir string, configPath string, reg *registry.Registry) (*schemaconfig.SchemaConfig, error) {
	if filepath.Base(configPath) == "schema.config.ts" {
		// The static read applies the config import rule (D34): the config
		// package, and other services' sentinels, which EnsureSentinels
		// writes before discovery.
		return tsreader.ReadServiceConfig(serviceDir, reg)
	}
	return schemaconfig.ReadFile(serviceDir, reg)
}

// EnsureSentinels writes the sentinel of every service under servicesRoot
// that lacks one or whose config's name or kind changed. A config may import
// a sibling's sentinel (D34), so discovery, which reads every config in
// full, needs them all on disk first: build-all, build --with-deps and
// migrate run this before DiscoverWith. The sweep reads only each config's
// name and kind, so it never needs a sentinel itself. log, when set,
// receives a line per sentinel written.
func EnsureSentinels(servicesRoot string, reg *registry.Registry, log io.Writer) error {
	return sentinel.EnsureSiblings(servicesRoot, sentinel.Options{
		ReadTSIdentity: func(servicePath string) (*schemaconfig.SchemaConfig, error) {
			return tsreader.ReadServiceIdentity(servicePath, reg)
		},
		Registry: reg,
		Log:      log,
	})
}

// TopologicalSort returns services ordered so each service's build
// dependencies (declared dependencies and authDb) precede it, and so do
// the APIs it calls wherever the calls form no cycle. APIs that call each
// other form one, which is not an error: the build orders outputs, not
// whole services, so each callee's SDK builds before the caller's API
// server (Steps). Such a tree is ordered by the build dependencies alone.
// A cycle of build dependencies cannot build, and the error names each of
// its edges.
func TopologicalSort(services []Service) ([]Service, error) {
	byName := make(map[string]Service, len(services))
	for _, service := range services {
		if _, ok := byName[service.Name]; ok {
			return nil, fmt.Errorf("duplicate schema service name %s", service.Name)
		}
		byName[service.Name] = service
	}
	if sorted, cycle := sortBy(services, byName, Service.dependencyNames); cycle == nil {
		return sorted, nil
	}
	sorted, cycle := sortBy(services, byName, Service.buildDependencyNames)
	if cycle != nil {
		return nil, cycleError(byName, cycle)
	}
	return sorted, nil
}

// sortBy orders services so the services edges names for each precede
// it, or returns the path to the first cycle it finds, whose last service
// repeats an earlier one.
func sortBy(services []Service, byName map[string]Service, edges func(Service) []string) ([]Service, []string) {
	visiting := make(map[string]bool, len(services))
	visited := make(map[string]bool, len(services))
	var result []Service
	var stack []string

	var visit func(Service) []string
	visit = func(service Service) []string {
		if visited[service.Name] {
			return nil
		}
		if visiting[service.Name] {
			return append(stack, service.Name)
		}
		visiting[service.Name] = true
		stack = append(stack, service.Name)
		for _, dep := range edges(service) {
			depService, ok := byName[dep]
			if !ok {
				continue
			}
			if cycle := visit(depService); cycle != nil {
				return cycle
			}
		}
		stack = stack[:len(stack)-1]
		visiting[service.Name] = false
		visited[service.Name] = true
		result = append(result, service)
		return nil
	}

	for _, service := range services {
		if cycle := visit(service); cycle != nil {
			return nil, cycle
		}
	}
	return result, nil
}

// cycleError describes the cycle of build dependencies at the end of
// path, whose last service repeats an earlier one, edge by edge: "a
// depends on b, b authenticates against a". Each edge names the config
// fields that make it.
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
	for i := 0; i+1 < len(cycle); i++ {
		from, to := cycle[i], cycle[i+1]
		var fields []string
		for _, ref := range byName[from].references() {
			if ref.name == to && ref.field != "calls" && !slices.Contains(fields, ref.field) {
				fields = append(fields, ref.field)
			}
		}
		verbs := make([]string, len(fields))
		for j, field := range fields {
			verbs[j] = edgeVerb(field)
		}
		edges = append(edges, fmt.Sprintf("%s %s %s", from, strings.Join(verbs, " and "), to))
	}
	return fmt.Errorf("circular dependency involving %s: %s", last, strings.Join(edges, ", "))
}

// edgeVerb phrases a config field as the relation it gives two services.
func edgeVerb(field string) string {
	switch field {
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

// Stage is the part of one service's build a step runs.
type Stage string

const (
	// StageAll builds every output of the service.
	StageAll Stage = "all"

	// StageBase builds every output but the API server: the types, the
	// SQL, the ORM and the SDKs.
	StageBase Stage = "base"

	// StageServer builds the API server, whose Deps imports the SDK of
	// each API the service calls.
	StageServer Stage = "server"
)

// Step is one step of a build: a stage of one service.
type Step struct {
	Service Service
	Stage   Stage
}

// Steps orders the build of services, as TopologicalSort or Closure
// ordered them, by output (docs/stack-model.md, section 3.3): a service
// whose callees are all built before it builds whole, in its place; one
// that calls an API built after it builds its base outputs in its place
// and its API server once the base outputs of every API it calls are
// built. So each SDK builds before the servers that import it, and two
// APIs may call each other. A callee outside services is not waited for.
func Steps(services []Service) []Step {
	present := make(map[string]bool, len(services))
	for _, service := range services {
		present[service.Name] = true
	}
	based := make(map[string]bool, len(services))
	ready := func(service Service) bool {
		for _, callee := range service.callNames() {
			if present[callee] && !based[callee] {
				return false
			}
		}
		return true
	}
	steps := make([]Step, 0, len(services))
	var waiting []Service
	for _, service := range services {
		based[service.Name] = true
		if ready(service) {
			steps = append(steps, Step{Service: service, Stage: StageAll})
		} else {
			steps = append(steps, Step{Service: service, Stage: StageBase})
			waiting = append(waiting, service)
		}
		still := waiting[:0]
		for _, caller := range waiting {
			if ready(caller) {
				steps = append(steps, Step{Service: caller, Stage: StageServer})
			} else {
				still = append(still, caller)
			}
		}
		waiting = still
	}
	return steps
}

// GroupIntoPhases groups steps into phases whose steps may run in
// parallel. A whole or base step waits for the base outputs of the
// service's build dependencies, and a whole one also for its callees'; a
// server step waits for its own base outputs and its callees'. A service
// in alreadyBuilt is built whole.
func GroupIntoPhases(steps []Step, alreadyBuilt map[string]bool) ([][]Step, error) {
	based := make(map[string]bool, len(alreadyBuilt)+len(steps))
	for name, ok := range alreadyBuilt {
		if ok {
			based[name] = true
		}
	}
	allBased := func(names []string) bool {
		for _, name := range names {
			if !based[name] {
				return false
			}
		}
		return true
	}
	ready := func(step Step) bool {
		switch step.Stage {
		case StageServer:
			return based[step.Service.Name] && allBased(step.Service.callNames())
		case StageBase:
			return allBased(step.Service.buildDependencyNames())
		default:
			return allBased(step.Service.buildDependencyNames()) && allBased(step.Service.callNames())
		}
	}

	remaining := append([]Step(nil), steps...)
	var phases [][]Step
	for len(remaining) > 0 {
		var phase []Step
		next := remaining[:0]
		for _, step := range remaining {
			if ready(step) {
				phase = append(phase, step)
			} else {
				next = append(next, step)
			}
		}
		if len(phase) == 0 {
			names := make([]string, 0, len(next))
			for _, step := range next {
				names = append(names, step.Service.Name)
			}
			sort.Strings(names)
			return nil, fmt.Errorf("circular dependency among %s", strings.Join(slices.Compact(names), ", "))
		}
		for _, step := range phase {
			if step.Stage != StageServer {
				based[step.Service.Name] = true
			}
		}
		remaining = next
		phases = append(phases, phase)
	}
	return phases, nil
}
