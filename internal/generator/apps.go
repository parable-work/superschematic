package generator

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	ir "github.com/parable-work/superschematic/ir"
)

// The apps of a stack (D78): each `@app` of a Rust API is a deployable
// whose entrypoint the stack's build writes beside the servers', and whose
// program is a library crate the project writes, at the naming file's
// [implementation_paths] rust_app template. The build scaffolds a missing
// crate once, with a serve that says it is not implemented, and refuses a
// crate that declares no serve, as it refuses an implementation without
// the functions its entrypoints call.

// appScaffold is the crate of an app the build writes, because it is
// missing, once the stack's plans pass.
type appScaffold struct {
	output *rustrestgen.APIOutput
	app    string
	dir    string
}

// write writes the scaffold, unless a crate is there by now.
func (sc appScaffold) write(r run) error {
	written, err := rustrestgen.WriteAppScaffold(sc.output, sc.app, sc.dir, APIDir(r.Options.OutputRoot, sc.output.SchemaName), r.Options.Naming)
	if err != nil {
		return fmt.Errorf("generator: the crate of app %s of %s: %w", sc.app, sc.output.SchemaName, err)
	}
	if written {
		r.Logf("  + app scaffold of %s of %s written to %s\n", sc.app, sc.output.SchemaName, sc.dir)
	}
	return nil
}

// planRustApp plans the entrypoint of a, an app of a Rust API of the
// stack, whose crate is at dir, and the scaffold of the crate when it is
// missing. It refuses a crate that exists and declares no serve.
func (r run) planRustApp(stackName string, a *ir.ResolvedDeployable, dir string) (*servergen.RustServer, *appScaffold, error) {
	impl, err := servergen.ReadRustApp(r.Options.Naming, a.App.API, a.App.Name, dir)
	if err != nil {
		return nil, nil, fmt.Errorf("stack %s: %w", stackName, err)
	}
	if impl.Manifest != nil {
		if err := r.checkRustApp(stackName, a, dir); err != nil {
			return nil, nil, err
		}
	}
	server, _, output, err := r.planRust(stackName, a, nil, &servergen.AppInput{Name: a.App.Name, Implementation: impl})
	if err != nil {
		return nil, nil, err
	}
	if impl.Manifest != nil {
		return server, nil, nil
	}
	return server, &appScaffold{output: output, app: a.App.Name, dir: dir}, nil
}

// checkRustApp refuses the crate of app a, at dir, whose src/lib.rs
// declares no serve, which the app's entrypoint calls: the build never
// writes into the crate, and cargo would fail only when it builds the
// app's entrypoint.
func (r run) checkRustApp(stackName string, a *ir.ResolvedDeployable, dir string) error {
	declares, err := rustrestgen.DeclaresFunc(dir, rustrestgen.ServeFunc)
	if err != nil || declares {
		return err
	}
	crate := strings.ReplaceAll(r.Options.Naming.RustAPICrate(a.App.API), "-", "_")
	parts := rustrestgen.AppInfo{Name: a.App.Name}.PartsName()
	return fmt.Errorf("stack %s: app %s of %s is the crate at %s, which declares no %s in %s; add\n\n"+
		"  pub async fn %s(parts: %s::%s::%s) -> std::io::Result<()>\n\n"+
		"which serves the app on parts.addr until the process should exit, with the API's Deps and implementations in parts (D78)",
		stackName, a.App.Name, a.App.API, dir, rustrestgen.ServeFunc, rustrestgen.ImplementationLib, rustrestgen.ServeFunc, crate, rustrestgen.AppsModule, parts)
}
