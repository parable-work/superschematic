package servergen

// TypeScriptGraph is the facade of a version graph a TypeScript
// entrypoint imports (D138): the class the database's types package
// exports from its ./versiongraph entry, and the name the entrypoint
// imports it as.
type TypeScriptGraph struct {
	// Facade is the class as the module exports it (RecipeGraph), and
	// Class the name the entrypoint imports it as.
	Facade string
	Class  string
	// Module is the module that exports it: the ./versiongraph entry of
	// the database's TypeScript types package.
	Module string
}

// TypeScriptGraphUse is a version graph whose operations an API serves
// (D138): the member of RouterOptions.graphs that holds its facade, the
// facade, and the name the entrypoint imports the version-graph package's
// pgPool as, which the facade is built over.
type TypeScriptGraphUse struct {
	Member string
	Graph  *TypeScriptGraph
	Pool   string
}

// graph is the facade named facade that module exports, as the entrypoint
// imports it: the one the database already holds, or a new one, whose
// name in the entrypoint taken makes unique.
func (d *TypeScriptDatabase) graph(facade, module string, taken names) *TypeScriptGraph {
	for _, g := range d.Graphs {
		if g.Facade == facade && g.Module == module {
			return g
		}
	}
	g := &TypeScriptGraph{Facade: facade, Class: taken.take(facade), Module: module}
	d.Graphs = append(d.Graphs, g)
	return g
}

// servesGraphs reports whether an API the server serves has a
// @graphOperations set (D138).
func (s *TypeScriptServer) servesGraphs() bool {
	return s.VersionGraphPool != ""
}
