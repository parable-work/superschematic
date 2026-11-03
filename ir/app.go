package ir

// App is an app an API service declares with `@app` on a class of its
// schema: the project's own HTTP program over the API's Deps, run where the
// API's servers run as a deployable of its own (D78). Its routes are the
// project's, and its program is a library the project writes, which the
// stack's build wraps in an entrypoint that builds the API's Deps and
// implementations as the API's server does. The class's name is the app's,
// and the class holds nothing else, so an app is no type: the loaders
// record it in Schema.Apps and in no other place, as they do a job (Job).
type App struct {
	// Name is the class's name: the app's `<Name>Parts` and the last part
	// of its deployable's name.
	Name string `json:"name" yaml:"name"`

	// Comment stores the node-attached comment of the class declaration.
	Comment string `json:"comment,omitempty" yaml:"comment,omitempty"`

	// Extensions name the superschematic extensions the app's build needs
	// (`["topcoat"]`), each the Name() of an extension. A binary that does
	// not link one resolves, plans and inits the stack as usual, and
	// refuses to build the app.
	Extensions []string `json:"extensions,omitempty" yaml:"extensions,omitempty"`
}

// App returns the app named name, or nil.
func (s *Schema) App(name string) *App {
	for _, app := range s.Apps {
		if app != nil && app.Name == name {
			return app
		}
	}
	return nil
}

// AppDeployableName names the deployable of app of the API service api as
// a job's is named (JobDeployableName): the API's name, a hyphen, and the
// app's name in kebab case (`shop-stock-backoffice`).
func AppDeployableName(api, app string) string {
	return JobDeployableName(api, app)
}
