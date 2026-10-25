package registry

import (
	"fmt"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// appDecorator returns @app, from @superschematic/api and only in API
// schemas: a class of the schema that declares an app of the API, the
// project's own HTTP program over the API's Deps, which the stack runs as
// a deployable of its own (D78). The class holds no fields and is no type:
// Apply records the app in Schema.Apps, and the TypeScript reader leaves
// the class out of Types.
func appDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "app", Packages: []string{pkgAPI}, Target: TargetType,
		Kinds: []string{string(ir.SchemaKindAPI)},
		Apply: func(n Node, args []any, _ Site) error {
			app, err := appOf(n.Type, args)
			if err != nil {
				return err
			}
			if n.Schema.App(app.Name) != nil {
				return fmt.Errorf("@app class %s is declared twice", app.Name)
			}
			n.Schema.Apps = append(n.Schema.Apps, app)
			return nil
		},
	}
}

// appOf reads @app({ extensions }) on td.
func appOf(td *ir.TypeDef, args []any) (*ir.App, error) {
	if len(td.Fields) > 0 {
		return nil, fmt.Errorf("@app class %s has fields; an app's program is the project's own crate, so its class holds none", td.Name)
	}
	if td.Extends != "" || len(td.Implements) > 0 {
		return nil, fmt.Errorf("@app class %s extends or implements another class; an app's class declares the app and nothing else", td.Name)
	}
	if len(args) > 1 {
		return nil, fmt.Errorf("@app takes one options object: { extensions }")
	}
	app := &ir.App{Name: td.Name, Comment: td.Comment}
	if len(args) == 0 || args[0] == nil {
		return app, nil
	}
	obj, ok := args[0].(map[string]any)
	if !ok {
		return nil, ArgErrorf(0, "@app takes an options object: { extensions }")
	}
	for key, value := range obj {
		switch key {
		case "extensions":
			list, ok := value.([]any)
			if !ok {
				return nil, ArgErrorf(0, "@app extensions is a list of the names of the extensions the app's build needs (\"topcoat\")")
			}
			for _, item := range list {
				name, ok := item.(string)
				if !ok {
					return nil, ArgErrorf(0, "@app extensions lists extensions by name, as strings (\"topcoat\")")
				}
				app.Extensions = append(app.Extensions, name)
			}
		default:
			return nil, ArgErrorf(0, "@app has no option %q; it takes extensions", key)
		}
	}
	if err := CheckApp(app); err != nil {
		return nil, ArgErrorf(0, "@app %s: %v", app.Name, err)
	}
	return app, nil
}

// CheckApp checks an app's declaration in every form: a name, and each
// extension named once, by a name an extension may take. Whether the
// binary links each extension is checked only where the app is built
// (stack.CheckAppExtensions).
func CheckApp(app *ir.App) error {
	if app.Name == "" {
		return fmt.Errorf("an app has no name")
	}
	seen := map[string]bool{}
	for _, ext := range app.Extensions {
		switch {
		case ext == "":
			return fmt.Errorf("names an extension with no name")
		case strings.Contains(ext, "."):
			return fmt.Errorf("names extension %q, and an extension's name has no dot", ext)
		case seen[ext]:
			return fmt.Errorf("names extension %s twice", ext)
		}
		seen[ext] = true
	}
	return nil
}
