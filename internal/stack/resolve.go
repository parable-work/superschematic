package stack

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// namePattern is the shape of a stack, environment or deployable name: it
// is a path segment of environment.json and a part of edge and resource
// IDs.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// parameterPattern is the shape of a parameter name.
var parameterPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Resolve resolves in.Environment of in.Stack against the platforms,
// connectors, targets and DNS platforms reg holds. It returns the resolved
// environment, or an *Errors with every failure it found. reg must be
// finalized.
func Resolve(reg *registry.Registry, in Input) (*ir.ResolvedEnvironment, error) {
	if in.Stack == nil {
		return nil, &Errors{Environment: in.Environment, List: []Error{{Code: CodeInvalidStack, Message: "no stack to resolve"}}}
	}
	if err := in.FieldNames.Validate(); err != nil {
		return nil, &Errors{Stack: in.Stack.Name, Environment: in.Environment, List: []Error{{Code: CodeInvalidStack, Message: err.Error()}}}
	}
	r := newResolver(reg, in)
	env := r.resolve()
	if len(r.errs.List) > 0 {
		return nil, r.errs
	}
	return env, nil
}

// Servers returns the servers of in.Stack, which no environment changes:
// each declared server and the default server of every API service the
// stack reaches and no declared server serves (section 3.2), sorted by
// name. Each has its Name, Declared, Services sorted by name, Calls and
// Language; the rest is the environment's to resolve. in.Environment is
// not read. The error is an *Errors with every failure found among the
// checks that decide the servers.
func Servers(in Input) ([]*ir.ResolvedDeployable, error) {
	if in.Stack == nil {
		return nil, &Errors{List: []Error{{Code: CodeInvalidStack, Message: "no stack to read servers from"}}}
	}
	in.Environment = ""
	r := newResolver(nil, in)
	r.indexServices()
	r.collectMembers()
	r.declareDeployables()
	r.defaultDeployables()
	r.resolveCalls()
	var servers []*ir.ResolvedDeployable
	for _, name := range sortedKeys(r.deployables) {
		res := r.deployables[name].res
		if res.Kind != ir.DeployableServer {
			continue
		}
		slices.SortFunc(res.Services, func(a, b ir.ServiceRef) int { return strings.Compare(a.Name, b.Name) })
		res.Language, _ = r.serverLanguage(res)
		servers = append(servers, res)
	}
	if r.failed() {
		return nil, r.errs
	}
	return servers, nil
}

func newResolver(reg *registry.Registry, in Input) *resolver {
	return &resolver{
		reg:         reg,
		in:          in,
		stack:       in.Stack,
		services:    map[string]*Service{},
		members:     map[string]bool{},
		declared:    map[string]*ir.DeployableDecl{},
		deployables: map[string]*deployable{},
		byService:   map[string]string{},
		edges:       map[string]*edge{},
		errs:        &Errors{Stack: in.Stack.Name, Environment: in.Environment},
	}
}

// resolver is one resolution's state.
type resolver struct {
	reg   *registry.Registry
	in    Input
	stack *ir.Stack
	errs  *Errors

	services map[string]*Service
	// members are the services the stack reaches.
	members map[string]bool
	// declared are the declared deployables by name.
	declared map[string]*ir.DeployableDecl

	env    effectiveEnv
	target registry.TargetSpec

	deployables map[string]*deployable
	// byService maps a member service to the deployable that hosts or
	// serves it.
	byService map[string]string
	edges     map[string]*edge

	// produced are the resources producers returned, in order.
	produced []produced
	// parentEnv is the parent environment, resolved once for the inherited
	// nodes' check.
	parentEnv *ir.ResolvedEnvironment
	parentErr error
	// serverWaves are the servers' rollout waves, callees first.
	serverWaves map[string]int
}

// deployable is one deployable while it is resolved.
type deployable struct {
	res      *ir.ResolvedDeployable
	decl     *ir.DeployableDecl
	settings settings
	platform registry.PlatformSpec
	// fields are a server's config fields by name.
	fields map[string]*field
}

// settings are a deployable's settings merged over the environment chain.
type settings struct {
	platform string
	values   map[string]any
	env      map[string]ir.EnvValue
	// envFrom names the environment that set each env key.
	envFrom map[string]string
}

// effectiveEnv is an environment with its parents' values merged in.
type effectiveEnv struct {
	// chain is the environment and its ancestors, the root first.
	chain      []*ir.Environment
	target     string
	values     map[string]any
	domain     string
	dns        *ir.DNSPlacement
	parameters []string
}

func (r *resolver) fail(code Code, format string, args ...any) {
	r.errs.List = append(r.errs.List, Error{Code: code, Message: fmt.Sprintf(format, args...)})
}

func (r *resolver) failed() bool { return len(r.errs.List) > 0 }

// resolve runs the stages in order. A stage that needs a sound model stops
// at the first stage that failed, so the model checks all report before any
// platform runs.
func (r *resolver) resolve() *ir.ResolvedEnvironment {
	r.indexServices()
	if !r.resolveEnvironment() {
		return nil
	}
	r.resolveTarget()
	r.collectMembers()
	r.declareDeployables()
	r.defaultDeployables()
	r.applySettings()
	r.place()
	r.expose()
	r.resolveCalls()
	if r.failed() {
		return nil
	}
	r.nameDeployables()
	r.deriveEdges()
	r.orderServers()
	r.bindConfig()
	if r.failed() {
		return nil
	}
	out := r.output()
	r.connectEdges()
	if r.failed() {
		return nil
	}
	r.lower(out)
	if r.failed() {
		return nil
	}
	r.checkGraph(out)
	if r.failed() {
		return nil
	}
	r.order(out)
	if r.failed() {
		return nil
	}
	r.checkPolicies(out)
	return out
}

func (r *resolver) indexServices() {
	for i := range r.in.Services {
		svc := &r.in.Services[i]
		if _, dup := r.services[svc.Name]; dup {
			r.fail(CodeInvalidStack, "service %s is given twice", svc.Name)
			continue
		}
		r.services[svc.Name] = svc
	}
	if !namePattern.MatchString(r.stack.Name) {
		r.fail(CodeInvalidStack, "stack name %q must be letters, digits, hyphens and underscores", r.stack.Name)
	}
}

// resolveEnvironment finds the environment, walks its `extends` chain and
// merges the chain's values. It returns false when there is no environment
// to resolve.
func (r *resolver) resolveEnvironment() bool {
	seenNames := map[string]bool{}
	for _, env := range r.stack.Environments {
		if env == nil {
			continue
		}
		if !namePattern.MatchString(env.Name) {
			r.fail(CodeInvalidStack, "environment name %q must be letters, digits, hyphens and underscores", env.Name)
		}
		if seenNames[env.Name] {
			r.fail(CodeInvalidStack, "stack %s declares environment %s twice", r.stack.Name, env.Name)
		}
		seenNames[env.Name] = true
	}
	env := r.stack.Environment(r.in.Environment)
	if env == nil {
		r.fail(CodeInvalidStack, "stack %s has no environment %s", r.stack.Name, r.in.Environment)
		return false
	}
	var chain []*ir.Environment
	visiting := map[string]bool{}
	for cur := env; cur != nil; {
		if visiting[cur.Name] {
			r.fail(CodeInvalidStack, "environment %s extends itself through %s", env.Name, cur.Name)
			return false
		}
		visiting[cur.Name] = true
		chain = append([]*ir.Environment{cur}, chain...)
		if cur.Extends == "" {
			break
		}
		parent := r.stack.Environment(cur.Extends)
		if parent == nil {
			r.fail(CodeInvalidStack, "environment %s extends %s, which stack %s does not declare", cur.Name, cur.Extends, r.stack.Name)
			return false
		}
		cur = parent
	}
	eff := effectiveEnv{chain: chain, values: map[string]any{}}
	seenParams := map[string]bool{}
	for _, e := range chain {
		if e.Target != "" {
			eff.target = e.Target
		}
		for key, value := range e.Values {
			eff.values[key] = deepCopy(value)
		}
		if e.Domain != "" {
			eff.domain = e.Domain
		}
		if e.DNS != nil {
			eff.dns = e.DNS
		}
		for _, param := range e.Parameters {
			if !parameterPattern.MatchString(param) {
				r.fail(CodeInvalidStack, "environment %s declares parameter %q; a parameter name is a letter or underscore and then letters, digits and underscores", e.Name, param)
				continue
			}
			if seenParams[param] {
				r.fail(CodeInvalidStack, "environment %s declares parameter %s, which it already has", e.Name, param)
				continue
			}
			seenParams[param] = true
			eff.parameters = append(eff.parameters, param)
		}
	}
	if eff.dns != nil && eff.domain == "" {
		r.fail(CodeInvalidStack, "environment %s places DNS on %s but sets no domain", env.Name, eff.dns.Platform)
	}
	r.env = eff
	return true
}

func (r *resolver) envName() string { return r.in.Environment }

func (r *resolver) resolveTarget() {
	if r.env.target == "" {
		r.fail(CodeUnknownTarget, "environment %s names no target", r.envName())
		return
	}
	target, ok := r.reg.Target(r.env.target)
	if !ok {
		r.fail(CodeUnknownTarget, "environment %s names target %s, which is not registered (registered targets: %v)", r.envName(), r.env.target, r.reg.Targets())
		return
	}
	r.target = target
	if err := target.ValidateValues(r.env.values); err != nil {
		r.fail(CodeInvalidValues, "environment %s: values for target %s: %v", r.envName(), target.Name, err)
	}
	if r.env.domain == "" {
		return
	}
	platform := target.DNS
	var values map[string]any
	if r.env.dns != nil {
		platform, values = r.env.dns.Platform, r.env.dns.Values
	}
	switch platform {
	case "", ir.ManualDNS:
		if len(values) > 0 {
			r.fail(CodeInvalidValues, "environment %s sets values for DNS, but no DNS platform holds its records", r.envName())
		}
		return
	}
	dns, ok := r.reg.DNSPlatform(platform)
	if !ok {
		r.fail(CodeUnknownPlatform, "environment %s places DNS on %s, which is not a registered DNS platform (registered DNS platforms: %v)", r.envName(), platform, r.reg.DNSPlatforms())
		return
	}
	if err := dns.ValidateValues(values); err != nil {
		r.fail(CodeInvalidValues, "environment %s: values for DNS platform %s: %v", r.envName(), dns.Name, err)
	}
}

// checkRef checks a handle: it names a service the input gives, of the
// kind it claims, and of one of the kinds its place takes.
func (r *resolver) checkRef(where string, ref ir.ServiceRef, want ...ir.SchemaKind) bool {
	svc, ok := r.services[ref.Name]
	if !ok {
		r.fail(CodeUnknownService, "%s names service %s, which is not among the stack's services", where, ref.Name)
		return false
	}
	if ref.Kind != svc.Kind {
		r.fail(CodeKindMismatch, "%s holds a handle to %s of kind %s, but %s is a %s service", where, ref.Name, ref.Kind, ref.Name, svc.Kind)
		return false
	}
	if len(want) > 0 && !slices.Contains(want, svc.Kind) {
		r.fail(CodeKindMismatch, "%s names %s, a %s service; it takes %s", where, ref.Name, svc.Kind, kindList(want))
		return false
	}
	return true
}

func kindList(kinds []ir.SchemaKind) string {
	names := make([]string, len(kinds))
	for i, kind := range kinds {
		names[i] = string(kind) + " services"
	}
	return strings.Join(names, " or ")
}

// collectMembers finds the services the stack reaches: its entry points,
// the services its declared deployables name, and everything those reach
// through `authDb`, DB dependencies and `calls` (section 4.1).
func (r *resolver) collectMembers() {
	var queue []string
	add := func(name string) {
		if !r.members[name] {
			r.members[name] = true
			queue = append(queue, name)
		}
	}
	for _, ref := range r.stack.Deploy {
		if r.checkRef("deploy", ref, ir.SchemaKindAPI, ir.SchemaKindDB) {
			add(ref.Name)
		}
	}
	for _, decl := range r.stack.Deployables {
		if decl == nil {
			continue
		}
		for _, ref := range decl.Serves {
			if r.checkRef(fmt.Sprintf("server %s serves", decl.Name), ref, ir.SchemaKindAPI) {
				add(ref.Name)
			}
		}
		for _, ref := range decl.Hosts {
			if r.checkRef(fmt.Sprintf("database %s hosts", decl.Name), ref, ir.SchemaKindDB) {
				add(ref.Name)
			}
		}
	}
	for len(queue) > 0 {
		svc := r.services[queue[0]]
		queue = queue[1:]
		if svc.Kind != ir.SchemaKindAPI {
			continue
		}
		if svc.AuthDB != nil && r.checkRef(fmt.Sprintf("service %s authDb", svc.Name), *svc.AuthDB, ir.SchemaKindDB) {
			add(svc.AuthDB.Name)
		}
		for _, dep := range svc.Dependencies {
			if dep.Kind != ir.SchemaKindDB {
				// Only DB dependencies join the stack; a handle of another
				// kind is still checked against a service the input gives.
				if other, ok := r.services[dep.Name]; ok && other.Kind != dep.Kind {
					r.checkRef(fmt.Sprintf("service %s dependencies", svc.Name), dep)
				}
				continue
			}
			if r.checkRef(fmt.Sprintf("service %s dependencies", svc.Name), dep, ir.SchemaKindDB) {
				add(dep.Name)
			}
		}
		for _, ref := range svc.Calls {
			if r.checkRef(fmt.Sprintf("service %s calls", svc.Name), ref, ir.SchemaKindAPI) {
				add(ref.Name)
			}
		}
	}
}

// declareDeployables records the declared deployables and the services
// each claims.
func (r *resolver) declareDeployables() {
	for _, decl := range r.stack.Deployables {
		if decl == nil {
			continue
		}
		if !namePattern.MatchString(decl.Name) {
			r.fail(CodeInvalidStack, "deployable name %q must be letters, digits, hyphens and underscores", decl.Name)
			continue
		}
		if _, dup := r.declared[decl.Name]; dup {
			r.fail(CodeInvalidStack, "stack %s declares deployable %s twice", r.stack.Name, decl.Name)
			continue
		}
		r.declared[decl.Name] = decl
		var claims []ir.ServiceRef
		switch decl.Kind {
		case ir.DeployableServer:
			if len(decl.Serves) == 0 {
				r.fail(CodeInvalidStack, "server %s serves no API", decl.Name)
			}
			if len(decl.Hosts) > 0 {
				r.fail(CodeInvalidStack, "server %s hosts DB schemas; a database hosts them", decl.Name)
			}
			claims = decl.Serves
		case ir.DeployableDatabase:
			if len(decl.Hosts) == 0 {
				r.fail(CodeInvalidStack, "database %s hosts no DB schema", decl.Name)
			}
			if len(decl.Serves) > 0 {
				r.fail(CodeInvalidStack, "database %s serves APIs; a server does", decl.Name)
			}
			claims = decl.Hosts
		default:
			r.fail(CodeInvalidStack, "deployable %s has kind %q (want %s or %s)", decl.Name, decl.Kind, ir.DeployableDatabase, ir.DeployableServer)
			continue
		}
		d := &deployable{
			res:  &ir.ResolvedDeployable{Name: decl.Name, Kind: decl.Kind, Declared: true},
			decl: decl,
		}
		r.deployables[decl.Name] = d
		for _, ref := range claims {
			svc, ok := r.services[ref.Name]
			if !ok || svc.Kind != ref.Kind {
				continue // collectMembers reported it
			}
			if prev, dup := r.byService[ref.Name]; dup {
				r.fail(CodeInvalidStack, "%s is claimed by both %s and %s; a service runs in one deployable", ref.Name, prev, decl.Name)
				continue
			}
			r.byService[ref.Name] = decl.Name
			d.res.Services = append(d.res.Services, ir.ServiceRef{Name: svc.Name, Kind: svc.Kind})
		}
	}
}

// defaultDeployables gives each member API service no declared server
// serves a server of its own, and each member DB service no declared
// database hosts a database of its own (section 3.2).
func (r *resolver) defaultDeployables() {
	for _, name := range sortedKeys(r.members) {
		if _, claimed := r.byService[name]; claimed {
			continue
		}
		svc := r.services[name]
		var kind ir.DeployableKind
		switch svc.Kind {
		case ir.SchemaKindAPI:
			kind = ir.DeployableServer
		case ir.SchemaKindDB:
			kind = ir.DeployableDatabase
		default:
			continue
		}
		if _, clash := r.deployables[name]; clash {
			r.fail(CodeInvalidStack, "the default %s of %s would be named %s, like the declared deployable %s", kind, name, name, name)
			continue
		}
		r.deployables[name] = &deployable{res: &ir.ResolvedDeployable{
			Name:     name,
			Kind:     kind,
			Services: []ir.ServiceRef{{Name: svc.Name, Kind: svc.Kind}},
		}}
		r.byService[name] = name
	}
}

// resolveDeployableRef finds the deployable a settings `of` or an
// `expose` entry names.
func (r *resolver) resolveDeployableRef(where string, ref ir.DeployableRef) (*deployable, bool) {
	switch {
	case ref.Service != nil && ref.Deployable != "":
		r.fail(CodeInvalidStack, "%s names both service %s and deployable %s", where, ref.Service.Name, ref.Deployable)
		return nil, false
	case ref.Service != nil:
		if !r.checkRef(where, *ref.Service, ir.SchemaKindAPI, ir.SchemaKindDB) {
			return nil, false
		}
		name, ok := r.byService[ref.Service.Name]
		if !ok {
			r.fail(CodeUnknownDeployable, "%s names service %s, which is not in stack %s", where, ref.Service.Name, r.stack.Name)
			return nil, false
		}
		return r.deployables[name], true
	case ref.Deployable != "":
		if _, ok := r.declared[ref.Deployable]; !ok {
			r.fail(CodeUnknownDeployable, "%s names deployable %s, which stack %s does not declare", where, ref.Deployable, r.stack.Name)
			return nil, false
		}
		d, ok := r.deployables[ref.Deployable]
		return d, ok
	}
	r.fail(CodeInvalidStack, "%s names no service and no deployable", where)
	return nil, false
}

// applySettings merges each deployable's settings over the environment
// chain, the root first: a later platform replaces an earlier one, and
// values and env keys merge key by key.
func (r *resolver) applySettings() {
	for _, env := range r.env.chain {
		seen := map[string]bool{}
		for i, entry := range env.Settings {
			if entry == nil {
				continue
			}
			where := fmt.Sprintf("environment %s settings[%d] (of %s)", env.Name, i, entry.Of)
			d, ok := r.resolveDeployableRef(where, entry.Of)
			if !ok {
				continue
			}
			if seen[d.res.Name] {
				r.fail(CodeInvalidStack, "environment %s sets %s twice", env.Name, d.res.Name)
				continue
			}
			seen[d.res.Name] = true
			s := &d.settings
			if entry.Platform != "" {
				s.platform = entry.Platform
			}
			for key, value := range entry.Values {
				if s.values == nil {
					s.values = map[string]any{}
				}
				s.values[key] = deepCopy(value)
			}
			for key, value := range entry.Env {
				if s.env == nil {
					s.env = map[string]ir.EnvValue{}
					s.envFrom = map[string]string{}
				}
				s.env[key] = value
				s.envFrom[key] = env.Name
			}
		}
	}
}

// place puts each deployable on its platform and checks the platform can
// realize it.
func (r *resolver) place() {
	for _, name := range sortedKeys(r.deployables) {
		d := r.deployables[name]
		res := d.res
		slices.SortFunc(res.Services, func(a, b ir.ServiceRef) int { return strings.Compare(a.Name, b.Name) })
		platformName := d.settings.platform
		if platformName == "" {
			if r.target.Name == "" {
				continue // resolveTarget reported it
			}
			platformName = r.target.Platforms[res.Kind]
			if platformName == "" {
				r.fail(CodeUnrealizable, "%s %s: target %s has no platform for a %s; place it with a settings platform", res.Kind, name, r.target.Name, res.Kind)
				continue
			}
		}
		platform, ok := r.reg.Platform(platformName)
		if !ok {
			r.fail(CodeUnknownPlatform, "%s %s is placed on platform %s, which is not registered (registered platforms: %v)", res.Kind, name, platformName, r.reg.Platforms())
			continue
		}
		if platform.Kind != res.Kind {
			r.fail(CodeUnrealizable, "%s %s is placed on platform %s, which realizes a %s", res.Kind, name, platformName, platform.Kind)
			continue
		}
		res.Platform = platformName
		d.platform = platform
		switch res.Kind {
		case ir.DeployableServer:
			r.placeServer(d)
		case ir.DeployableDatabase:
			r.placeDatabase(d)
		}
		if len(d.settings.values) > 0 {
			res.Settings = d.settings.values
		}
		if err := platform.ValidateSettings(d.settings.values); err != nil {
			r.fail(CodeInvalidSettings, "settings of %s on platform %s: %v", name, platformName, err)
		}
	}
}

func (r *resolver) placeServer(d *deployable) {
	lang, ok := r.serverLanguage(d.res)
	if !ok {
		return
	}
	d.res.Language = lang
	if !slices.Contains(d.platform.Languages, d.res.Language) {
		r.fail(CodeUnrealizable, "server %s is a %s server, and platform %s runs only %s", d.res.Name, d.res.Language, d.platform.Name, strings.Join(d.platform.Languages, ", "))
	}
}

// serverLanguage returns the language of a server: that of the APIs it
// serves, Go for an API that names none. It reports false, and fails the
// resolution, when the APIs are written in more than one.
func (r *resolver) serverLanguage(res *ir.ResolvedDeployable) (string, bool) {
	languages := map[string][]string{}
	for _, ref := range res.Services {
		lang := r.services[ref.Name].Language
		if lang == "" {
			lang = registry.APILanguageGo
		}
		languages[lang] = append(languages[lang], ref.Name)
	}
	if len(languages) > 1 {
		var parts []string
		for _, lang := range sortedKeys(languages) {
			parts = append(parts, fmt.Sprintf("%s in %s", strings.Join(languages[lang], ", "), lang))
		}
		r.fail(CodeUnrealizable, "server %s serves %s; one process runs one language", res.Name, strings.Join(parts, " and "))
		return "", false
	}
	for lang := range languages {
		return lang, true
	}
	return "", true
}

func (r *resolver) placeDatabase(d *deployable) {
	for _, dialect := range d.platform.Dialects {
		supported := true
		for _, ref := range d.res.Services {
			dialects := r.services[ref.Name].Dialects
			if len(dialects) == 0 {
				dialects = []string{registry.SQLDialectPostgres}
			}
			if !slices.Contains(dialects, dialect) {
				supported = false
				break
			}
		}
		if supported {
			d.res.Dialect = dialect
			return
		}
	}
	var hosted []string
	for _, ref := range d.res.Services {
		dialects := r.services[ref.Name].Dialects
		if len(dialects) == 0 {
			dialects = []string{registry.SQLDialectPostgres}
		}
		hosted = append(hosted, fmt.Sprintf("%s (%s)", ref.Name, strings.Join(dialects, ", ")))
	}
	r.fail(CodeUnrealizable, "database %s hosts %s, and platform %s runs only %s", d.res.Name, strings.Join(hosted, ", "), d.platform.Name, strings.Join(d.platform.Dialects, ", "))
}

// expose marks the deployables reachable from outside the environment.
func (r *resolver) expose() {
	for i, ref := range r.stack.Expose {
		d, ok := r.resolveDeployableRef(fmt.Sprintf("expose[%d] (%s)", i, ref), ref)
		if !ok {
			continue
		}
		if d.res.Kind != ir.DeployableServer {
			r.fail(CodeExposeNotServer, "stack %s exposes %s, a %s; only a server is exposed", r.stack.Name, d.res.Name, d.res.Kind)
			continue
		}
		d.res.Exposed = true
	}
}

// resolveCalls gives each server the APIs it calls: the union of the
// `calls` of the APIs it serves. A call to an API the same server serves
// stays a call, to the server's own address.
func (r *resolver) resolveCalls() {
	for _, name := range sortedKeys(r.deployables) {
		d := r.deployables[name]
		if d.res.Kind != ir.DeployableServer {
			continue
		}
		seen := map[string]bool{}
		for _, served := range d.res.Services {
			for _, ref := range r.services[served.Name].Calls {
				svc, ok := r.services[ref.Name]
				if !ok || svc.Kind != ir.SchemaKindAPI || ref.Kind != svc.Kind || seen[ref.Name] {
					continue // collectMembers reported a bad handle
				}
				seen[ref.Name] = true
				d.res.Calls = append(d.res.Calls, ir.ServiceRef{Name: svc.Name, Kind: svc.Kind})
			}
		}
		slices.SortFunc(d.res.Calls, func(a, b ir.ServiceRef) int { return strings.Compare(a.Name, b.Name) })
	}
}

// stackEnvironment is the environment as platforms see it.
func (r *resolver) stackEnvironment() registry.StackEnvironment {
	env := r.env.chain[len(r.env.chain)-1]
	return registry.StackEnvironment{
		Stack:      r.stack.Name,
		Name:       env.Name,
		Extends:    env.Extends,
		Target:     r.env.target,
		Values:     deepCopy(r.env.values).(map[string]any),
		Domain:     r.env.domain,
		Parameters: slices.Clone(r.env.parameters),
	}
}

// nameDeployables asks each platform for its deployable's name and
// address.
func (r *resolver) nameDeployables() {
	for _, name := range sortedKeys(r.deployables) {
		d := r.deployables[name]
		ctx := registry.PlatformContext{Environment: r.stackEnvironment(), Deployable: cloneDeployable(d.res)}
		where := fmt.Sprintf("platform %s names %s with", d.platform.Name, name)
		d.res.ResourceName = r.normalize(where, d.platform.NameOf(ctx))
		r.checkParameters(where, d.res.ResourceName)
		ctx = registry.PlatformContext{Environment: r.stackEnvironment(), Deployable: cloneDeployable(d.res)}
		where = fmt.Sprintf("platform %s addresses %s with", d.platform.Name, name)
		d.res.Address = r.normalize(where, d.platform.AddressOf(ctx))
		r.checkParameters(where, d.res.Address)
	}
}

// normalize returns a value a platform, connector or DNS platform
// produced in the form environment.json reads back: it encodes the value
// and decodes it again, so typed Go containers become maps and lists,
// every reference becomes an Output, Parameter or Concat, and the resolver
// holds its own copy. A value that does not encode, or whose references
// are malformed, fails.
func (r *resolver) normalize(where string, v any) any {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err == nil {
		var raw any
		if err = json.Unmarshal(data, &raw); err == nil {
			if v, err = ir.DecodeValue(raw); err == nil {
				return v
			}
		}
	}
	r.fail(CodeLowering, "%s a value environment.json cannot hold: %v", where, err)
	return nil
}

// checkParameters reports a parameter v references that the environment
// does not declare.
func (r *resolver) checkParameters(where string, v any) {
	_, params := ir.ValueRefs(v)
	for _, param := range params {
		if !slices.Contains(r.env.parameters, param) {
			r.fail(CodeUnknownParameter, "%s parameter %s, which environment %s does not declare", where, param, r.envName())
		}
	}
}

// output assembles the resolved environment from the resolved model.
func (r *resolver) output() *ir.ResolvedEnvironment {
	env := r.env.chain[len(r.env.chain)-1]
	out := &ir.ResolvedEnvironment{
		Version:     ir.ResolvedEnvironmentVersion,
		Stack:       r.stack.Name,
		Environment: env.Name,
		Extends:     env.Extends,
		Target:      r.target.Name,
		Provisioner: r.target.Provisioner,
		Parameters:  slices.Clone(r.env.parameters),
		Domain:      r.env.domain,
		Deployables: []*ir.ResolvedDeployable{},
		Resources:   &ir.ResourceGraph{Parameters: slices.Clone(r.env.parameters), Resources: []*ir.Resource{}},
	}
	if len(r.env.values) > 0 {
		out.Values = deepCopy(r.env.values).(map[string]any)
	}
	for _, name := range sortedKeys(r.deployables) {
		out.Deployables = append(out.Deployables, r.deployables[name].res)
	}
	for _, id := range sortedKeys(r.edges) {
		out.Edges = append(out.Edges, r.edges[id].res)
	}
	out.Secrets = r.secrets()
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// cloneDeployable copies a deployable for a platform, deep enough that the
// platform cannot change the resolver's copy.
func cloneDeployable(d *ir.ResolvedDeployable) ir.ResolvedDeployable {
	c := *d
	c.Services = slices.Clone(d.Services)
	c.Calls = slices.Clone(d.Calls)
	if d.Settings != nil {
		c.Settings = deepCopy(d.Settings).(map[string]any)
	}
	c.ResourceName = deepCopy(d.ResourceName)
	c.Address = deepCopy(d.Address)
	if d.Bindings != nil {
		c.Bindings = make([]*ir.Binding, len(d.Bindings))
		for i, b := range d.Bindings {
			copied := *b
			copied.Value = deepCopy(b.Value)
			c.Bindings[i] = &copied
		}
	}
	return c
}

// deepCopy copies the maps, lists and concatenations of a JSON-shaped
// value.
func deepCopy(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, inner := range v {
			out[key] = deepCopy(inner)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, inner := range v {
			out[i] = deepCopy(inner)
		}
		return out
	case ir.Concat:
		out := make(ir.Concat, len(v))
		for i, inner := range v {
			out[i] = deepCopy(inner)
		}
		return out
	}
	return v
}
