package servergen

import (
	"strings"

	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
)

// The runtime parts of an API's database in a Rust entrypoint (#620, #622,
// #617, #610): main.rs asks the implementation's create_wiring for its
// Wire, builds the API crate's Wired over the database's pool, its config
// and its providers, and fills Deps with the notifier, the dispatcher and
// the inbound path Wired holds. A server's then wraps the implementation's
// authenticator in the grants' and the access log (Wired::handlers),
// merges the parts' routes beside the API's router (Wired::routes) and
// holds its MCP endpoint's calls to an agent run's autonomy
// (Wired::mcp_gate); a worker of a queue a part handles runs it through
// Wired (rustrestgen's RuntimeWorkers), with no create_workers.

// RustWiring is how main.rs builds an API's runtime parts.
type RustWiring struct {
	// Wired is the variable of the API crate's Wired, Wire the
	// implementation's Wire, and Deps the WiringDeps both are built over.
	Wired string
	Wire  string
	Deps  string

	// Providers is the variable of the API's runtime providers, which
	// WiringDeps and Deps share, empty for an API with none.
	Providers string

	// Parts are the fields of Deps Wired fills: notifier, agents and
	// inbound, those the database has.
	Parts []string

	// Handlers is whether a server wraps the implementation's
	// authenticator (Wired::handlers), Routes the variable of the parts'
	// routes it merges, Gate whether its MCP endpoint takes the
	// dispatcher's call gate, and Tools whether it serves the calendar's
	// and the search's tools (#617). All are a server's alone.
	Handlers bool
	Routes   string
	Gate     bool
	Tools    bool
}

// planRustWiring plans the runtime parts of a's API, o, when its crate
// builds them: a job's, a worker's and a deriver's Deps hold them too, and
// a server mounts their routes.
func planRustWiring(o *rustrestgen.APIOutput, a *RustAPI, taken rustNames, stem string, background bool) {
	w := o.Wiring()
	if w == nil || a.Database == nil {
		return
	}
	wiring := &RustWiring{
		Wired: taken.take(stem + "_wired"),
		Wire:  taken.take(stem + "_wire"),
		Deps:  taken.take(stem + "_wiring_deps"),
	}
	if a.Providers {
		wiring.Providers = taken.take(stem + "_providers")
	}
	if w.Notifier() {
		wiring.Parts = append(wiring.Parts, "notifier")
	}
	if w.Agents() {
		wiring.Parts = append(wiring.Parts, "agents")
	}
	if w.Inbound {
		wiring.Parts = append(wiring.Parts, "inbound")
	}
	if !background {
		wiring.Handlers = o.WiringAuthenticates()
		wiring.Routes = taken.take(stem + "_wired_routes")
		wiring.Gate = w.Agents() && o.MCP != nil
		wiring.Tools = (len(w.Search) > 0 || w.Calendar) && o.MCP != nil
	}
	a.Wiring = wiring
}

// ProvidersInit is the expression Deps.providers is filled with: the
// providers WiringDeps shares, or those the API's config configures.
func (a *RustAPI) ProvidersInit() string {
	if a.Wiring != nil && a.Wiring.Providers != "" {
		return "Arc::clone(&" + a.Wiring.Providers + ")"
	}
	return "std::sync::Arc::new(configure(" + rustString(a.Service) + ", " + a.Alias + "::providers::from_config(&" + a.Config + "))?)"
}

// WiringLets are main.rs's statements that build the API's runtime parts,
// before its Deps: its providers, when it has them, the WiringDeps, the
// implementation's Wire, and Wired. shared says whether the config is
// cloned into WiringDeps: on a server, which reads it again, and in a
// background entrypoint, whose Deps takes it after.
func (a *RustAPI) WiringLets() string {
	w := a.Wiring
	const indent = "    "
	var b strings.Builder
	b.WriteString(indent + "// Its database's runtime parts (#620, #622, #617, #610), which its\n")
	b.WriteString(indent + "// implementation's create_wiring shapes.\n")
	if w.Providers != "" {
		b.WriteString(a.providersLet() + "\n")
	}
	b.WriteString(indent + "let " + w.Deps + " = " + a.Alias + "::WiringDeps {\n")
	b.WriteString(indent + indent + "db: " + a.Database.Var + ".clone(),\n")
	if a.Config != "" {
		b.WriteString(indent + indent + "config: " + a.Config + ".clone(),\n")
	}
	if w.Providers != "" {
		b.WriteString(indent + indent + "providers: Arc::clone(&" + w.Providers + "),\n")
	}
	b.WriteString(indent + "};\n")
	b.WriteString(rustLet(w.Wire, a.ImplAlias+"::"+rustrestgen.WiringFunc+"(&"+w.Deps+")", ".await", "map_err(|err| format!(\"wiring of "+a.Service+": {err}\"))?") + "\n")
	b.WriteString(rustLet(w.Wired, a.Alias+"::Wired::new(&"+w.Deps+", "+w.Wire+")", "map_err(|err| format!(\"runtime parts of "+a.Service+": {err}\"))?"))
	return b.String()
}

// WiredFields are the fields of the API's Deps literal that Wired fills,
// indented as the literal's.
func (a *RustAPI) WiredFields() []string {
	if a.Wiring == nil {
		return nil
	}
	fields := make([]string, len(a.Wiring.Parts))
	for i, part := range a.Wiring.Parts {
		fields[i] = "        " + part + ": " + a.Wiring.Wired + "." + part + ".clone(),"
	}
	return fields
}

// WiredHandlersLet is a server's statement that wraps the handlers'
// authenticator as Wired says (#610).
func (a *RustAPI) WiredHandlersLet() string {
	return rustLetChain(a.Handlers, a.Wiring.Wired, "handlers("+a.Handlers+")", "map_err(|err| format!(\"authenticator of "+a.Service+": {err}\"))?")
}

// WiredRoutesLet is a server's statement that builds the parts' routes,
// whose callers the API's Implementations establish.
func (a *RustAPI) WiredRoutesLet() string {
	return rustLet(a.Wiring.Routes, a.Wiring.Wired+".routes(&"+a.Implementations+")")
}

// McpOptions is the router options' mcp field's value: the endpoint's
// options with the confirmation key (D93), the dispatcher's call gate
// (D183, #622) and the calendar's and the search's tools (#617), as
// rustfmt lays the chain out from indent.
func (a *RustAPI) McpOptions(indent string) string {
	root := "EndpointOptions::default()"
	var calls []string
	if a.MCPConfirmationKey != "" {
		calls = append(calls, ".with_confirmation_key("+a.MCPConfirmationKey+".as_str())")
	}
	if a.Wiring != nil && a.Wiring.Gate {
		calls = append(calls, ".with_call_gate("+a.Wiring.Wired+".mcp_gate())")
	}
	if a.Wiring != nil && a.Wiring.Tools {
		calls = append(calls, ".with_tools("+a.Wiring.Wired+".mcp_tools())")
	}
	field := indent + "mcp: " + root + strings.Join(calls, "") + ","
	if len(calls) == 1 && len(field) <= rustWidth {
		return field
	}
	return indent + "mcp: " + root + "\n" + indent + "    " + strings.Join(calls, "\n"+indent+"    ") + ","
}

// WiresMcp reports whether the API's MCP endpoint takes options of its
// runtime parts: the dispatcher's call gate or the calendar's and the
// search's tools.
func (a *RustAPI) WiresMcp() bool {
	return a.Wiring != nil && (a.Wiring.Gate || a.Wiring.Tools)
}

// RuntimeRun is a worker's statement that runs a queue a part of the API's
// database handles through Wired, keeping its outcome in worked when the
// worker closes anything after.
func (s *RustServer) RuntimeRun() string {
	wired, run := s.APIs[0].Wiring.Wired, s.Worker.RunFunc+"(options, shutdown)"
	if s.Closes() {
		return rustLetChain("worked", wired, run, "await")
	}
	const indent = "    "
	if chain := wired + "." + run + ".await?"; len(chain) <= rustChainWidth {
		return indent + chain + ";"
	}
	return indent + wired + "\n" + indent + indent + "." + run + "\n" + indent + indent + ".await?;"
}

// providersLet is the statement that configures the API's providers once,
// for WiringDeps and Deps, as rustfmt lays it out: on one line when it
// fits, and else the configure call's arguments a line each inside the
// Arc::new it overflows into.
func (a *RustAPI) providersLet() string {
	const indent = "    "
	service, config := rustString(a.Service), a.Alias+"::providers::from_config(&"+a.Config+")"
	if line := indent + "let " + a.Wiring.Providers + " = Arc::new(configure(" + service + ", " + config + ")?);"; len(line) <= rustWidth {
		return line
	}
	return indent + "let " + a.Wiring.Providers + " = Arc::new(configure(\n" + indent + indent + service + ",\n" + indent + indent + config + ",\n" + indent + ")?);"
}

// CallerProvidersLet is a server's statement that keeps Deps' providers
// before the implementation's create takes Deps (#744).
func (a *RustAPI) CallerProvidersLet() string {
	return rustLet(a.CallerProviders, "Arc::clone(&"+a.Deps+".providers)")
}

// CallerHandlersLet is a server's statement that wraps the handlers'
// authenticator, the wired one or the implementation's, in the API crate's
// providers::caller_authenticator, outermost (#744).
func (a *RustAPI) CallerHandlersLet() string {
	const indent = "    "
	var b strings.Builder
	b.WriteString(indent + "let " + a.Handlers + " = " + a.Alias + "::Handlers {\n")
	b.WriteString(indent + indent + "authenticator: " + a.Alias + "::providers::caller_authenticator(\n")
	b.WriteString(indent + indent + indent + "&" + a.CallerProviders + ",\n")
	b.WriteString(indent + indent + indent + a.Handlers + ".authenticator,\n")
	b.WriteString(indent + indent + "),\n")
	if a.HandlersRest {
		b.WriteString(indent + indent + ".." + a.Handlers + "\n")
	}
	b.WriteString(indent + "};")
	return b.String()
}
