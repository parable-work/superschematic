package local_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stack/local"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The app deployable kind on the local target (D78): shop-orders, in Rust,
// declares Backoffice, the project's own HTTP program over shop-orders'
// Deps. stack dev runs it as a process, as a Rust server, on a port of its
// own, ready once it listens there.

// backofficeApp is the deployable of shop-orders' app Backoffice.
const backofficeApp = "shop-orders-backoffice"

// backoffice names the app in settings and in expose.
var backoffice = stacktest.AppOf(stacktest.ShopOrders, "Backoffice")

// appShop is rustOrdersShop with shop-orders declaring the app Backoffice.
func appShop() []stack.Service {
	services := rustOrdersShop()
	for i := range services {
		if services[i].Name == stacktest.ShopOrders.Name {
			services[i].Apps = []ir.App{{Name: "Backoffice", Extensions: []string{"topcoat"}}}
		}
	}
	return services
}

// TestAppProcess: an app is placed on the app platform and lowered to a
// process of kind app: the entrypoint crate at server/<stack>/<app>, in
// Rust, on the port its settings give or one derived as a server's is,
// ready once it listens, with its API's environment. Its address is the
// process's loopback URL, also its public one when the stack exposes it,
// and the provisioner's program reads it as an app's process with a URL.
func TestAppProcess(t *testing.T) {
	reg := assemble(t)
	s := rustOrdersStack()
	s.Expose = append(s.Expose, backoffice)
	env := resolve(t, reg, s, appShop(), "Dev")
	d := env.Deployable(backofficeApp)
	if d == nil || d.Kind != ir.DeployableApp || d.Platform != local.AppPlatform || d.Language != registry.APILanguageRust || d.Artifact != ir.ArtifactImage {
		t.Fatalf("the app is %+v", d)
	}
	senv := registry.StackEnvironment{Stack: env.Stack, Name: env.Environment, SchemasRoot: checkoutRoot}
	port := local.ServerPort(senv, *d)
	url := local.ServerURL(port)
	if d.Address != url || d.PublicAddress != url {
		t.Errorf("the app's address is %v and its public address %v, want %s", d.Address, d.PublicAddress, url)
	}
	for _, e := range env.Edges {
		if e.From != backofficeApp {
			continue
		}
		want := map[ir.EdgeKind]string{ir.EdgeSQL: local.AppSQLConnector, ir.EdgeHTTP: local.AppHTTPConnector, ir.EdgeBucket: local.AppBucketConnector}[e.Kind]
		if e.Connector != want {
			t.Errorf("edge %s is connected by %s, want %s", e.ID, e.Connector, want)
		}
	}
	process := env.Resources.Resource(backofficeApp + ".process")
	if process == nil {
		t.Fatalf("no process for %s", backofficeApp)
	}
	want := map[string]any{
		"name":      backofficeApp,
		"module":    "server/rust-orders-stack/" + backofficeApp,
		"language":  "rust",
		"kind":      "app",
		"port":      port,
		"readiness": local.ReadinessListening,
	}
	for key, value := range want {
		if got := process.Properties[key]; fmt.Sprint(got) != fmt.Sprint(value) {
			t.Errorf("the app's process has %s %v, want %v", key, got, value)
		}
	}
	if got, want := serverVariable(t, env, backofficeApp, "SHOP_DB_DATABASE_URL"), serverVariable(t, env, "Orders", "SHOP_DB_DATABASE_URL"); got != want {
		t.Errorf("the app's SHOP_DB_DATABASE_URL = %v, want its API's server's, %v", got, want)
	}
	prog, err := local.ProgramOf(env)
	if err != nil {
		t.Fatal(err)
	}
	var app *local.Server
	for _, s := range prog.Servers {
		if s.Deployable == backofficeApp {
			app = s
		}
	}
	if app == nil {
		t.Fatalf("the program runs no %s: %+v", backofficeApp, prog.Servers)
	}
	if app.Kind != "app" || app.Port != port || app.URL != url || app.Readiness != local.ReadinessListening || app.Binary != "" {
		t.Errorf("the app's process = %+v", app)
	}

	// Its port setting, as a server's.
	s.Environments[0].Settings = append(s.Environments[0].Settings, &ir.DeployableSettings{Of: backoffice, Values: map[string]any{"port": float64(8090)}})
	env = resolve(t, reg, s, appShop(), "Dev")
	if got := env.Resources.Resource(backofficeApp + ".process").Properties["port"]; fmt.Sprint(got) != "8090" {
		t.Errorf("the app's port with a port setting = %v, want 8090", got)
	}
	if got := env.Deployable(backofficeApp).Address; got != local.ServerURL(8090) {
		t.Errorf("the app's address with a port setting = %v", got)
	}

	// The platform's settings are a server's: a port alone.
	s.Environments[0].Settings[len(s.Environments[0].Settings)-1].Values = map[string]any{"minInstances": float64(1)}
	errs := resolveErr(t, s, appShop(), "Dev")
	wantFailure(t, errs, stack.CodeInvalidSettings, "settings of "+backofficeApp+" on platform "+local.AppPlatform)
}

// TestAppTurnedOff: an app the environment turns off stays in the
// environment, exposed or not, with its edges, but runs no process and has
// no address, and nothing references one.
func TestAppTurnedOff(t *testing.T) {
	s := rustOrdersStack()
	s.Expose = append(s.Expose, backoffice)
	off := false
	s.Environments[0].Settings = append(s.Environments[0].Settings, &ir.DeployableSettings{Of: backoffice, Enabled: &off})
	env := resolve(t, assemble(t), s, appShop(), "Dev")
	d := env.Deployable(backofficeApp)
	if d == nil || d.App == nil || d.App.Enabled {
		t.Fatalf("the app is %+v, want it in the environment and off", d)
	}
	if d.Address != nil || d.PublicAddress != nil {
		t.Errorf("an app turned off has address %v and public address %v", d.Address, d.PublicAddress)
	}
	if res := env.Resources.Resource(backofficeApp + ".process"); res != nil {
		t.Errorf("an app turned off has a process: %v", res.Properties)
	}
	edges := 0
	for _, e := range env.Edges {
		if e.From == backofficeApp {
			edges++
		}
	}
	if edges == 0 {
		t.Error("an app turned off lost its edges")
	}
	prog, err := local.ProgramOf(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range prog.Servers {
		if s.Deployable == backofficeApp {
			t.Errorf("the program runs an app turned off: %+v", s)
		}
	}
}

// TestAppIdentityConfig: an app of an API over the user model (D50) binds
// the API's identity field from the platform's identity config, as the
// API's server does: a session cookie without Secure, since the process
// serves plain HTTP.
func TestAppIdentityConfig(t *testing.T) {
	services := appShop()
	for i := range services {
		if services[i].Name == stacktest.ShopOrders.Name {
			services[i].Identity = true
		}
	}
	env := resolve(t, assemble(t), rustOrdersStack(), services, "Dev")
	for _, name := range []string{backofficeApp, "Orders"} {
		var identity *ir.Binding
		for _, b := range env.Deployable(name).Bindings {
			if b.IdentityOf == "shop-orders" {
				identity = b
			}
		}
		if identity == nil {
			t.Errorf("%s binds no identity field of shop-orders", name)
			continue
		}
		if identity.Value != `{"cookie":{"secure":false}}` {
			t.Errorf("%s's identity config = %v, want a cookie without Secure", name, identity.Value)
		}
	}
}

// TestApplyRunsAnApp applies Dev with shop-orders' app: its wave builds
// the app's crate with cargo in its module, starts the binary its
// Cargo.toml names with its port in PORT and its API's database, and
// waits until it listens there. An app that never listens fails the
// rollout and says where it should have.
func TestApplyRunsAnApp(t *testing.T) {
	appCrate := binCrate("rust-orders-stack-"+backofficeApp+"-app", backofficeApp)
	t.Run("listening", func(t *testing.T) {
		f := newFixtureWith(t, rustOrdersStack(), appShop(), "Dev")
		f.writeRustOrders(t)
		f.writeCrate(t, backofficeApp, appCrate)
		f.runner.listens = map[string]bool{backofficeApp: true}
		f.runner.rules = append(f.runningContainers("postgres:16-alpine"),
			rule{prefix: migrateStatus, stderr: "superschematic-migrate: service shop-db has no applied model"},
			rule{prefix: "/bin/superschematic-migrate apply", out: "applied 2 steps\n"},
		)
		if err := f.applyAll(t); err != nil {
			t.Fatalf("%v\n%s", err, f.out)
		}
		defer func() { _ = f.prov.Destroy(context.Background(), f.req) }()
		target, err := filepath.Abs(filepath.Join(f.req.Dir, local.RustTargetDir))
		if err != nil {
			t.Fatal(err)
		}
		var app *fakeProcess
		for _, p := range f.runner.started {
			if p.cmd.Path == filepath.Join(target, "release", backofficeApp) {
				app = p
			}
		}
		if app == nil || app.cmd.Dir != filepath.Join(f.req.OutputRoot, local.ModulePath(f.env.Stack, backofficeApp)) {
			t.Fatalf("the app started as %+v, want its binary in its module", app)
		}
		port := local.ServerPort(f.senv(), *f.env.Deployable(backofficeApp))
		if got := envValue(app.cmd.Env, local.PortVariable); got != strconv.Itoa(port) {
			t.Errorf("the app runs with PORT=%q, want %d", got, port)
		}
		if got, want := envValue(app.cmd.Env, "SHOP_DB_DATABASE_URL"), local.DatabaseURL(f.pgPort, "shop_db"); got != want {
			t.Errorf("the app's SHOP_DB_DATABASE_URL = %q, want %q", got, want)
		}
		for _, want := range []string{
			"build " + backofficeApp + ": cargo build --release --locked in server/rust-orders-stack/" + backofficeApp,
			backofficeApp + " is ready at " + local.ServerURL(port),
		} {
			if !strings.Contains(f.out.String(), want) {
				t.Errorf("output lacks %q:\n%s", want, f.out)
			}
		}
		for url := range f.runner.probes {
			if strings.HasPrefix(url, local.ServerURL(port)) {
				t.Errorf("the provisioner probed the app at %s; an app answers no readiness path", url)
			}
		}
	})
	t.Run("never listening", func(t *testing.T) {
		f := newFixtureWith(t, rustOrdersStack(), appShop(), "Dev")
		f.keys(t)
		f.writeRustOrders(t)
		f.writeCrate(t, backofficeApp, appCrate)
		err := f.rollout(t, backofficeApp+".process")
		defer func() { _ = f.prov.Destroy(context.Background(), f.req) }()
		port := local.ServerPort(f.senv(), *f.env.Deployable(backofficeApp))
		want := "local: app " + backofficeApp + " does not listen at " + local.ServerURL(port) + " after 1s; it listens on the port in PORT"
		if err == nil || err.Error() != want {
			t.Errorf("got %v, want %s", err, want)
		}
	})
}
