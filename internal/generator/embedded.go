package generator

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/embedded"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	ir "github.com/parable-work/superschematic/ir"
)

// The crates of the environments of a stack that build as one binary
// (D116): an environment some of whose servers or workers run inside its
// binary (ir.ArtifactBinary) gets a crate at
// `<output-root>/embedded/<stack>/<environment>/`, a member of the output
// root's Cargo workspace beside the servers' crates, which
// `superschematic stack build <environment>` builds (servergen's
// embedded.go).

// EmbeddedDefaultPort is the port an environment's binary listens on when
// PORT is unset and the environment's values name none.
const EmbeddedDefaultPort = 8080

// planEmbedded plans the crate of each environment of the stack in envs
// that builds as one binary, in declaration order. It refuses an
// environment that holds, beside its binary, a server, a job or a worker
// that runs outside it.
func (r run) planEmbedded(stackName string, envs []*ir.ResolvedEnvironment) ([]*servergen.EmbeddedCrate, error) {
	var crates []*servergen.EmbeddedCrate
	for _, env := range envs {
		if !env.IsBinary() {
			continue
		}
		in, err := r.embeddedInput(stackName, env)
		if err != nil {
			return nil, err
		}
		// The plans the environment's releases recorded are a chain its
		// binary can carry, each as it was recorded (D116): a build refuses
		// one a hand changed, as it refuses a projection's stamp that names
		// other files (D102).
		if r.Options.ServicePath != "" {
			for _, d := range in.Databases {
				if _, err := embedded.ReadRecorded(r.Options.ServicePath, env.Environment, d.Service); err != nil {
					return nil, fmt.Errorf("stack %s: environment %s: %w", stackName, env.Environment, err)
				}
			}
		}
		crate, err := servergen.PlanEmbedded(in)
		if err != nil {
			return nil, err
		}
		crates = append(crates, crate)
	}
	return crates, nil
}

// embeddedInput reads what the crate of env's binary is planned from: its
// servers with the APIs each serves, its workers that run, the file of
// each DB schema its databases host, the variables it binds, and its
// site.
func (r run) embeddedInput(stackName string, env *ir.ResolvedEnvironment) (servergen.EmbeddedInput, error) {
	out := r.Options.OutputRoot
	where := fmt.Sprintf("stack %s: environment %s builds as one binary (D116)", stackName, env.Environment)
	in := servergen.EmbeddedInput{
		Stack:              stackName,
		Environment:        env.Environment,
		Dir:                servergen.EmbeddedDir(out, stackName, env.Environment),
		OutputRoot:         out,
		Port:               embeddedPort(env),
		Naming:             r.Options.Naming,
		RepositoryRoot:     r.Options.Naming.BuildContext(r.Options.RepositoryRoot),
		ImplementationRoot: r.Options.RepositoryRoot,
		SDKDirs:            map[string]string{},
		Paths:              r.Options.Paths,
	}
	edges := map[string]*ir.Edge{}
	for _, e := range env.Edges {
		edges[e.ID] = e
	}
	databases := map[string]bool{}
	for _, d := range env.Deployables {
		switch {
		case d.Kind == ir.DeployableDatabase:
			databases[d.Name] = true
		case d.Kind == ir.DeployableSite:
			site := &servergen.EmbeddedSiteInput{Name: d.Name}
			if d.Site != nil {
				site.Fallback = d.Site.Fallback
			}
			for _, b := range d.Bindings {
				site.APIs = append(site.APIs, b.Field)
			}
			slices.Sort(site.APIs)
			if in.Site != nil {
				return in, fmt.Errorf("%s, and has sites %s and %s: a binary answers every path that is no API's from one site", where, in.Site.Name, d.Name)
			}
			in.Site = site
		case !d.Kind.RunsCode():
		case !d.InBinary():
			return in, fmt.Errorf("%s, and %s %s runs outside it, on platform %s: an environment that builds a binary holds every server and worker in it", where, d.Kind, d.Name, d.Platform)
		case d.Kind == ir.DeployableWorker:
			if d.Worker != nil && d.Worker.Instances > 0 {
				in.Workers = append(in.Workers, d.Name)
			}
		case d.Kind == ir.DeployableServer:
			server := servergen.EmbeddedServerInput{Name: d.Name}
			for _, ref := range d.Services {
				api, err := r.embeddedAPI(stackName, d, ref.Name)
				if err != nil {
					return in, err
				}
				for _, call := range api.Output.Deps.Calls {
					in.SDKDirs[call.Service] = SDKDir(out, LangRust, call.Service)
				}
				server.APIs = append(server.APIs, api)
			}
			in.Servers = append(in.Servers, server)
			bound, err := embeddedBound(d, edges)
			if err != nil {
				return in, fmt.Errorf("%s: server %s: %w", where, d.Name, err)
			}
			in.Bound = append(in.Bound, bound...)
		default:
			return in, fmt.Errorf("%s, and holds %s %s, which a binary does not run", where, d.Kind, d.Name)
		}
	}
	// A database's node names the file of each DB schema it hosts, under
	// the binary's data directory.
	for _, res := range env.Resources.Resources {
		service, _ := res.Properties["service"].(string)
		file, _ := res.Properties["file"].(string)
		if service == "" || file == "" || !slices.ContainsFunc(res.Owners, func(owner string) bool { return databases[owner] }) {
			continue
		}
		in.Databases = append(in.Databases, servergen.EmbeddedDatabaseInput{Service: service, File: file})
	}
	slices.SortFunc(in.Databases, func(a, b servergen.EmbeddedDatabaseInput) int { return strings.Compare(a.Service, b.Service) })
	return in, nil
}

// embeddedAPI reads one API a server of an environment's binary serves, as
// planRust reads a Rust server's: its crate, its routes, its EnvConfig
// and its implementation, which the plan of the stack's servers scaffolds
// when it is missing.
func (r run) embeddedAPI(stackName string, d *ir.ResolvedDeployable, service string) (servergen.RustAPIInput, error) {
	served, err := r.servedRun(stackName, d.Name, service)
	if err != nil {
		return servergen.RustAPIInput{}, err
	}
	output, config, err := served.rustAPI()
	if err != nil {
		return servergen.RustAPIInput{}, err
	}
	if output == nil {
		return servergen.RustAPIInput{}, fmt.Errorf("stack %s: server %s serves %s, which declares no operations", stackName, d.Name, service)
	}
	routes, err := served.APIOutput()
	if err != nil {
		return servergen.RustAPIInput{}, err
	}
	if routes.Engine != nil {
		return servergen.RustAPIInput{}, fmt.Errorf("stack %s: server %s serves %s, which connects to %s, an engine database, and runs inside a binary (D116), which opens no engine: an engine on a SQLite file is not built", stackName, d.Name, service, routes.Engine.Database)
	}
	dir := r.Options.Naming.RustImplementationDir(r.Options.RepositoryRoot, service)
	impl, err := servergen.ReadRustImplementation(r.Options.Naming, service, dir)
	if err != nil {
		return servergen.RustAPIInput{}, err
	}
	return servergen.RustAPIInput{
		Output:         output,
		Routes:         routes,
		Config:         config,
		Dir:            APIDir(r.Options.OutputRoot, service),
		Implementation: impl,
	}, nil
}

// embeddedBound are the variables server d's bindings set, as a platform
// sets them on a process (docs/stack-model.md, section 3.4): a literal as
// its field, and a derived field as one variable per member of its value.
// It leaves out a secret, which the binary never carries and the process's
// environment holds; a CORS field, since the site and the APIs share one
// origin; and a member that references what only a running environment
// knows. A callers field is bound with no issuers: its issuers are the
// loopback keys the process generates as it starts.
func embeddedBound(d *ir.ResolvedDeployable, edges map[string]*ir.Edge) ([]servergen.EmbeddedVariable, error) {
	var out []servergen.EmbeddedVariable
	for _, b := range d.Bindings {
		switch b.Source {
		case ir.BindingLiteral:
			out = append(out, servergen.EmbeddedVariable{Name: b.Field, Value: literalVariable(b.Value), Server: d.Name})
		case ir.BindingDerived:
			if b.CallersOf != "" {
				// The field's own issuers are none: the binary adds each
				// calling server's loopback issuer as it starts.
				vars, err := ir.DerivedVariables(b.Field, ir.ServiceAuth{Issuers: []*ir.ServiceAuthIssuer{}})
				if err != nil {
					return nil, fmt.Errorf("binding %s: %w", b.Field, err)
				}
				for _, v := range vars {
					if value, plain := v.Value.(string); plain {
						out = append(out, servergen.EmbeddedVariable{Name: v.Name, Value: value, Server: d.Name})
					}
				}
				continue
			}
			if b.CORSOf != "" {
				continue
			}
			vars, err := ir.DerivedVariables(b.Field, b.Value)
			if err != nil {
				return nil, fmt.Errorf("binding %s: %w", b.Field, err)
			}
			sql := edges[b.Edge] != nil && edges[b.Edge].Kind == ir.EdgeSQL
			for _, v := range vars {
				value, plain := v.Value.(string)
				if !plain || v.Secret {
					continue
				}
				if sql && !strings.HasPrefix(value, "sqlite:") {
					return nil, fmt.Errorf("binding %s holds %s for %s, which is no SQLite file: a binary's database is a file under its data directory", b.Field, v.Name, edges[b.Edge].Service.Name)
				}
				out = append(out, servergen.EmbeddedVariable{Name: v.Name, Value: value, Server: d.Name})
			}
		}
	}
	return out, nil
}

// literalVariable is a literal binding's value as an environment variable
// holds it: a string as it is, a whole number as its decimal, and any
// other value as its JSON.
func literalVariable(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
	case nil:
		return ""
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}

// embeddedPort is the port env's binary listens on when PORT is unset: its
// values' port, or EmbeddedDefaultPort.
func embeddedPort(env *ir.ResolvedEnvironment) int {
	switch port := env.Values["port"].(type) {
	case int:
		return port
	case int64:
		return int(port)
	case float64:
		return int(port)
	case json.Number:
		if n, err := port.Int64(); err == nil {
			return int(n)
		}
	}
	return EmbeddedDefaultPort
}

// writeEmbedded writes the crate of each environment in crates, removes
// the crate of each environment of the stack that builds as one binary no
// longer, and says what each binary does not serve.
func (r run) writeEmbedded(stackName string, crates []*servergen.EmbeddedCrate) error {
	keep := make([]string, len(crates))
	for i, c := range crates {
		keep[i] = c.Environment
		dir := servergen.EmbeddedDir(r.Options.OutputRoot, stackName, c.Environment)
		if err := servergen.WriteEmbedded(c, dir); err != nil {
			return err
		}
		r.Logf("  - environment %s: its binary's crate at %s; superschematic stack build %s builds it\n", c.Environment, dir, c.Environment)
		for _, note := range c.Notes {
			r.Logf("  - environment %s: %s\n", c.Environment, note)
		}
	}
	return servergen.PruneEmbedded(r.Options.OutputRoot, stackName, keep)
}
