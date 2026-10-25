package stack

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The apps of a stack (D78). Each `@app` of a member API service is a
// deployable of kind app, named after its API and its class as a job is
// (ir.AppDeployableName). Its edges, its config and its identity are its
// API's: the resolver treats an app as it treats a job wherever a
// deployable runs code (ir.DeployableKind.RunsCode), and as a server
// where one serves requests, so it is exposed as a server is and binds
// its API's identity and callers fields. What is its own is decided here:
// the extensions its build needs, and whether it runs. An app runs on a
// Rust API.

// AppLanguages says which languages an app runs in (D78), for the refusals
// of a Go or TypeScript API's app.
const AppLanguages = "an app runs on a Rust API; Go and TypeScript follow"

// Apps returns the apps of in.Stack, which no environment changes: an app
// per `@app` of every API service the stack reaches, sorted by name. Each
// has its Name, Services (its API), Calls and Buckets (its API's),
// Language and App's API, Name and Extensions; the rest is the
// environment's to resolve. in.Environment is not read. The error is an
// *Errors with every failure found among the checks that decide the apps,
// a Go or TypeScript API's app among them.
func Apps(in Input) ([]*ir.ResolvedDeployable, error) {
	if in.Stack == nil {
		return nil, &Errors{List: []Error{{Code: CodeInvalidStack, Message: "no stack to read apps from"}}}
	}
	in.Environment = ""
	r := newResolver(nil, in)
	r.indexServices()
	r.collectMembers()
	r.declareDeployables()
	r.defaultDeployables()
	r.defaultWorkers()
	r.defaultDerivers()
	r.defaultApps()
	r.resolveCalls()
	r.resolveBuckets()
	r.runApps()
	var apps []*ir.ResolvedDeployable
	for _, name := range sortedKeys(r.deployables) {
		res := r.deployables[name].res
		if res.Kind != ir.DeployableApp {
			continue
		}
		res.Language, _ = r.serverLanguage(res)
		apps = append(apps, res)
	}
	if r.failed() {
		return nil, r.errs
	}
	return apps, nil
}

// CheckAppExtensions refuses to build an app that needs an extension the
// running binary does not link (D78). linked are the names of the linked
// extensions (registry.Registry.Extensions) and apps are what Apps
// returns. Resolution, plans and `stack init` read an app without it, so
// that a binary that never builds the app still runs them. Each command
// that builds the stack calls it first and refuses the whole stack: the
// stack's build, which writes the app's entrypoint (build-all and `stack
// dev`), and the commands that build its deployables' images from what a
// build wrote (`stack build`, and `stack deploy` and `stack prepare`
// unless --no-build). The error names each missing extension of each app,
// and the binary that links it.
func CheckAppExtensions(apps []*ir.ResolvedDeployable, linked []string) error {
	var msgs []string
	for _, d := range apps {
		if d == nil || d.App == nil {
			continue
		}
		for _, ext := range d.App.Extensions {
			if !slices.Contains(linked, ext) {
				msgs = append(msgs, fmt.Sprintf("app %s of %s needs the %s extension, which this superschematic binary does not link; run superschematic-%s", d.App.Name, d.App.API, ext, ext))
			}
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	errs := &Errors{}
	for _, msg := range msgs {
		errs.List = append(errs.List, Error{Code: CodeUnrealizable, Message: msg})
	}
	return errs
}

// defaultApps makes each app of a member API service a deployable of its
// own, named after its API and its class.
func (r *resolver) defaultApps() {
	if r.byApp == nil {
		r.byApp = map[string]string{}
	}
	for _, name := range sortedKeys(r.members) {
		svc := r.services[name]
		if svc.Kind != ir.SchemaKindAPI {
			continue
		}
		for i := range svc.Apps {
			app := &svc.Apps[i]
			key := name + "/" + app.Name
			if _, dup := r.byApp[key]; dup {
				r.fail(CodeInvalidStack, "service %s declares app %s twice", name, app.Name)
				continue
			}
			appName := ir.AppDeployableName(name, app.Name)
			if !namePattern.MatchString(appName) {
				r.fail(CodeInvalidStack, "app %s of %s would be named %q; a deployable's name is letters, digits, hyphens and underscores", app.Name, name, appName)
				continue
			}
			if other, clash := r.deployables[appName]; clash {
				r.fail(CodeInvalidStack, "app %s of %s would be named %s, like the %s %s; rename the app or the %s", app.Name, name, appName, other.res.Kind, appName, other.res.Kind)
				continue
			}
			r.deployables[appName] = &deployable{
				res: &ir.ResolvedDeployable{
					Name:     appName,
					Kind:     ir.DeployableApp,
					Services: []ir.ServiceRef{{Name: svc.Name, Kind: svc.Kind}},
					App:      &ir.ResolvedApp{API: name, Name: app.Name, Extensions: slices.Clone(app.Extensions)},
				},
				app: app,
			}
			r.byApp[key] = appName
		}
	}
}

// resolveAppRef finds the app a settings `of` or an `expose` entry names
// beside its API's handle.
func (r *resolver) resolveAppRef(where string, ref ir.DeployableRef) (*deployable, bool) {
	switch {
	case ref.Service == nil:
		r.fail(CodeInvalidStack, "%s names app %s but no API service; an app is named beside its API's handle", where, ref.App)
		return nil, false
	case ref.Job != "" || ref.Worker != "" || ref.Deriver != "":
		r.fail(CodeInvalidStack, "%s names app %s and a job, a worker or a deriver; an element names one", where, ref.App)
		return nil, false
	}
	if !r.checkRef(where, *ref.Service, ir.SchemaKindAPI) {
		return nil, false
	}
	if !r.members[ref.Service.Name] {
		r.fail(CodeUnknownDeployable, "%s names service %s, which is not in stack %s", where, ref.Service.Name, r.stack.Name)
		return nil, false
	}
	name, ok := r.byApp[ref.Service.Name+"/"+ref.App]
	if !ok {
		var apps []string
		for _, app := range r.services[ref.Service.Name].Apps {
			apps = append(apps, app.Name)
		}
		declared := "none"
		if len(apps) > 0 {
			declared = strings.Join(apps, ", ")
		}
		r.fail(CodeUnknownDeployable, "%s names app %s of %s, which declares no such @app class (its apps: %s)", where, ref.App, ref.Service.Name, declared)
		return nil, false
	}
	return r.deployables[name], true
}

// applyAppSettings merges an app's settings of one element, from
// environment env: enabled alone. It reports whether d is an app, whose
// element it was; a schedule, a time zone, onDeploy, instances,
// maxInstances or concurrency on one is refused.
func (r *resolver) applyAppSettings(where, env string, d *deployable, entry *ir.DeployableSettings) bool {
	if d.res.Kind != ir.DeployableApp {
		return false
	}
	if entry.Schedule != "" || entry.TimeZone != "" {
		r.fail(CodeInvalidSettings, "%s sets a schedule or a time zone on app %s, which serves until it is stopped; only a job takes them", where, d.res.Name)
	}
	if entry.OnDeploy != nil {
		r.fail(CodeInvalidSettings, "%s sets onDeploy on app %s, which serves until it is stopped; only a job runs on each deploy", where, d.res.Name)
	}
	if entry.Instances != nil || entry.MaxInstances != nil || entry.Concurrency != nil {
		r.fail(CodeInvalidSettings, "%s sets instances, maxInstances or concurrency on app %s; an app takes enabled, and its platform's settings say how it scales", where, d.res.Name)
	}
	if entry.Enabled != nil {
		s := &d.settings
		if s.workerFrom == nil {
			s.workerFrom = map[string]string{}
		}
		enabled := *entry.Enabled
		s.enabled, s.workerFrom["enabled"] = &enabled, env
	}
	return true
}

// runApps decides what each app runs in the environment (D78): it checks
// the declaration, refuses a Go or TypeScript API's app, and runs the app
// unless the settings turn it off.
func (r *resolver) runApps() {
	for _, name := range sortedKeys(r.deployables) {
		d := r.deployables[name]
		if d.res.Kind != ir.DeployableApp || d.app == nil {
			continue
		}
		run := d.res.App
		if err := registry.CheckApp(d.app); err != nil {
			r.fail(CodeInvalidStack, "app %s of %s: %v", d.app.Name, run.API, err)
			continue
		}
		if lang := r.services[run.API].Language; lang != registry.APILanguageRust {
			if lang == "" {
				lang = registry.APILanguageGo
			}
			r.fail(CodeUnrealizable, "app %s of %s: %s is a %s API, and %s; write the API in Rust", d.app.Name, run.API, run.API, lang, AppLanguages)
			continue
		}
		run.Enabled = d.settings.enabled == nil || *d.settings.enabled
	}
}
