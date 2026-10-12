package rustrestgen

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
)

// The implementation side of a Rust API (D57; docs/stack-model.md, section
// 8.5): src/deps.rs declares Deps, what the implementation is built from,
// Handlers, what its create returns, and Implementations::new, which joins
// the handlers with what the server's entrypoint builds, the twin of the
// Go API's deps.go and the TypeScript API's deps.ts; the scaffold writes
// the implementation's crate once, at the naming file's
// [implementation_paths] rust template.

// TracingVersion is the tracing crate Deps.span is of: the HTTP runtime's
// version, without its default features, as the runtime takes it.
const TracingVersion = "0.1.41"

// DepsInfo is what Deps holds beside the config and the span: a pool of
// the API's database and a Rust SDK client per API it calls. A bucket is
// not built for a Rust server (D57).
type DepsInfo struct {
	// Database is the DB service whose pool Deps.db is: the API's authDb,
	// or its one DB-kind dependency. Empty when the API has neither.
	Database string

	// Calls are the API's calls entries, in order.
	Calls []DepsCall
}

// DepsCall is a Rust SDK client in Deps.
type DepsCall struct {
	// Service is the called API service.
	Service string

	// Field is the Deps field that holds the client.
	Field string

	// Crate is the service's Rust SDK crate, and Path its directory
	// relative to the API crate's, which Cargo.toml depends on.
	Crate string
	Path  string

	// Client is the SDK's struct, which its new builds from a
	// ClientConfig.
	Client string
}

// CrateIdent is the SDK crate as Rust source spells it.
func (c DepsCall) CrateIdent() string {
	return strings.ReplaceAll(c.Crate, "-", "_")
}

// depsFields are the fields every Deps may hold beside its clients.
var depsFields = []string{"config", "db", "span"}

// SetDeps sets what Deps holds, so the crate writes src/deps.rs, refusing
// a client whose field would take the name of another of its fields.
func (o *APIOutput) SetDeps(deps DepsInfo) error {
	seen := map[string]string{}
	for _, name := range depsFields {
		seen[name] = "Deps." + name
	}
	for _, call := range deps.Calls {
		if prev, clash := seen[call.Field]; clash {
			return fmt.Errorf("rustrestgen: %s calls %s, whose client would be Deps.%s, the field of %s", o.SchemaName, call.Service, call.Field, prev)
		}
		seen[call.Field] = call.Service
	}
	o.Deps = &deps
	return nil
}

// DepsField is the Deps field of the client of the API service named
// service: its name in snake_case (shop-api is shop_api).
func DepsField(service string) string {
	return rustutil.ToSnakeCase(service)
}

// ChecksEndUsers reports whether an operation needs a caller that the
// implementation establishes: Handlers then holds the authenticator. An
// API whose authDb has a User table has the identity runtime establish
// every end user (D50), whose authenticator the entrypoint builds.
func (o *APIOutput) ChecksEndUsers() bool {
	return o.HasAuth && o.Identity == nil
}

// HandlersFields are the fields of Handlers: one per namespace, the
// authenticator when the implementation establishes the end user, and the
// webhook verifiers.
func (o *APIOutput) HandlersFields() []string {
	var fields []string
	for _, ns := range o.Namespaces {
		fields = append(fields, rustutil.ToSnakeCase(ns))
	}
	if o.ChecksEndUsers() {
		fields = append(fields, "authenticator")
	}
	if len(o.WebhookProviders) > 0 {
		fields = append(fields, "webhook_verifiers")
	}
	return fields
}

// DepsUses are the use declarations of src/deps.rs beside std's: the
// config, what it names from the interfaces module (each namespace's trait,
// Implementations and the webhook verifier's trait), and the runtime's
// pool and authenticators.
func (o *APIOutput) DepsUses() []string {
	var uses []string
	if o.HasEnvConfig {
		uses = append(uses, "use crate::config::EnvConfig;")
	}
	interfaces := []string{"Implementations"}
	for _, ns := range o.Namespaces {
		interfaces = append(interfaces, rustutil.ToPascalCase(ns)+"Implementation")
	}
	if len(o.WebhookProviders) > 0 {
		interfaces = append(interfaces, "WebhookVerifier")
	}
	uses = append(uses, rustutil.UseLine("crate::interfaces", interfaces))
	var runtime []string
	if o.Deps != nil && o.Deps.Database != "" {
		runtime = append(runtime, "postgres::Pool")
	}
	if o.ChecksEndUsers() {
		runtime = append(runtime, "Authenticator")
	}
	if o.Identity != nil {
		runtime = append(runtime, "IdentityAuthenticator")
	}
	if o.HasServiceCallers {
		runtime = append(runtime, "ServiceAuthenticator")
	}
	if len(runtime) > 0 {
		uses = append(uses, rustutil.UseLine(o.RuntimeCrateIdent, runtime))
	}
	return uses
}

// NewSignature is the signature of Implementations::new, indented as a
// method of an impl: the handlers, the identity runtime's authenticator for
// an API over the user model (D50) and the service authenticator for an
// API with a service clause (D37), on one line when it fits.
func (o *APIOutput) NewSignature() string {
	params := []string{"handlers: Handlers"}
	if o.Identity != nil {
		params = append(params, "authenticator: Arc<IdentityAuthenticator>")
	}
	if o.HasServiceCallers {
		params = append(params, "service_authenticator: Arc<dyn ServiceAuthenticator>")
	}
	return rustFn("    ", "pub fn new", params, "Self")
}

// NotImplemented is the body of endpoint's method in the implementation's
// impl: a 501 naming the method, laid out as rustfmt lays the call out.
func (s ImplementationScaffold) NotImplemented(endpoint EndpointInfo) string {
	message := rustString(rustutil.ToSnakeCase(endpoint.Namespace) + "." + endpoint.FunctionName + " is not implemented")
	if call := "ApiError::not_implemented(" + message + ")"; len(call) <= 60 {
		return "        Err(" + call + ")"
	}
	return "        Err(ApiError::not_implemented(\n            " + message + ",\n        ))"
}

// rustFn is a function's signature through its opening brace, indented by
// indent: on one line when it fits in 100 columns, as rustfmt lays it out,
// and otherwise a parameter to a line.
func rustFn(indent, name string, params []string, result string) string {
	line := indent + name + "(" + strings.Join(params, ", ") + ") -> " + result + " {"
	if len(line) <= 100 {
		return line
	}
	var b strings.Builder
	b.WriteString(indent + name + "(\n")
	for _, param := range params {
		b.WriteString(indent + "    " + param + ",\n")
	}
	b.WriteString(indent + ") -> " + result + " {")
	return b.String()
}

// RuntimeFeatures are the HTTP runtime's features the crate turns on:
// identity for the user model (D50); postgres, Deps.db's pool, for an API
// with a database, and identity-postgres beside both, so the identity store
// runs over the same pool; and http-client for an API that calls another,
// whose credential may be a Google ID token (D57).
func (o *APIOutput) RuntimeFeatures() []string {
	var features []string
	if o.Identity != nil {
		features = append(features, "identity")
	}
	if o.Deps == nil {
		return features
	}
	if o.Deps.Database != "" {
		features = append(features, "postgres")
		if o.Identity != nil {
			features = append(features, "identity-postgres")
		}
	}
	if len(o.Deps.Calls) > 0 {
		features = append(features, "http-client")
	}
	return features
}

// ImplementationScaffold is the template view of the implementation
// crate's scaffold.
type ImplementationScaffold struct {
	*APIOutput
	// Crate is the implementation crate's name
	// (Naming.RustImplementationCrate).
	Crate string
	// APIPath and RuntimePath are the API crate's and the HTTP runtime's
	// directories relative to the implementation's, which its Cargo.toml
	// depends on.
	APIPath     string
	RuntimePath string
}

// CrateIdent is the API crate as Rust source spells it.
func (s ImplementationScaffold) CrateIdent() string {
	return strings.ReplaceAll(s.CrateName, "-", "_")
}

// UsesTypes reports whether a method signature of the implementation names
// the types crate.
func (s ImplementationScaffold) UsesTypes() bool {
	for _, endpoint := range s.Endpoints {
		if endpoint.UsesTypes() {
			return true
		}
	}
	return false
}

// APIUse is the use declaration of what the implementation names from the
// API crate: Deps, Handlers, each namespace's trait and each Args struct
// its methods take.
func (s ImplementationScaffold) APIUse() string {
	names := []string{"Deps", "Handlers"}
	for _, ns := range s.Namespaces {
		names = append(names, rustutil.ToPascalCase(ns)+"Implementation")
	}
	for _, endpoint := range s.Endpoints {
		if endpoint.HasArgs() {
			names = append(names, endpoint.ArgsName)
		}
	}
	return rustutil.UseLine(s.CrateIdent(), names)
}

// RuntimeUse is the use declaration of what the implementation names from
// the HTTP runtime: the error and the request context its methods take,
// and the authenticator's trait and principal when it establishes the end
// user. Empty when it names none.
func (s ImplementationScaffold) RuntimeUse() string {
	var names []string
	if len(s.Namespaces) > 0 || s.ChecksEndUsers() {
		names = append(names, "ApiError")
	}
	if len(s.Namespaces) > 0 {
		names = append(names, "RequestContext")
	}
	if s.ChecksEndUsers() {
		names = append(names, "Authenticator", "Principal")
	}
	if len(names) == 0 {
		return ""
	}
	return rustutil.UseLine(s.RuntimeCrateIdent, names)
}

// MethodSignature is the signature of endpoint's method in the
// implementation's impl of its namespace's trait.
func (s ImplementationScaffold) MethodSignature(endpoint EndpointInfo) string {
	params := []string{"&self", "_ctx: RequestContext"}
	if endpoint.HasArgs() {
		params = append(params, "_args: "+endpoint.ArgsName)
	}
	return rustFn("    ", "async fn "+endpoint.FunctionName, params, "Result<"+endpoint.OutputRustType+", ApiError>")
}

// ImplementationManifest is the manifest the scaffold writes; a directory
// that holds one is an implementation, which the scaffold never writes into.
const ImplementationManifest = "Cargo.toml"

// ImplementationLib is the file the scaffold writes the implementation in.
var ImplementationLib = filepath.Join("src", "lib.rs")

// WriteImplementationScaffold writes the scaffold of the API's Rust
// implementation into dir, the crate the naming file's
// [implementation_paths] rust template places it in, when the crate is
// missing: when dir holds no Cargo.toml (D57). It never writes into a
// crate that exists, so it never overwrites the engineer's code, and
// reports whether it wrote. Its Cargo.toml depends on the API crate at
// apiDir and on the HTTP runtime at paths.HTTPRuntimeRust, by paths
// relative to dir, and its create(deps) returns the Handlers, each method
// answering 501 Not Implemented. The output must have its Deps
// (SetDeps).
func WriteImplementationScaffold(output *APIOutput, dir, apiDir string, paths naming.LocalPaths, names naming.Naming) (bool, error) {
	if output.Deps == nil {
		return false, fmt.Errorf("rustrestgen: the implementation scaffold of %s needs its Deps", output.SchemaName)
	}
	exists, err := ImplementationExists(dir)
	if err != nil || exists {
		return false, err
	}
	if paths.HTTPRuntimeRust == "" {
		return false, fmt.Errorf("rustrestgen: [paths] http_runtime_rust is required to locate the http-runtime crate from the implementation of %s", output.SchemaName)
	}
	view := ImplementationScaffold{APIOutput: output, Crate: names.OrDefault().RustImplementationCrate(output.SchemaName)}
	if view.APIPath, err = naming.RelPath(dir, apiDir); err != nil {
		return false, fmt.Errorf("rustrestgen: the API crate's path from the implementation of %s: %w", output.SchemaName, err)
	}
	if view.RuntimePath, err = naming.RelPath(dir, paths.HTTPRuntimeRust); err != nil {
		return false, fmt.Errorf("rustrestgen: the runtime crate's path from the implementation of %s: %w", output.SchemaName, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		return false, fmt.Errorf("create implementation crate %s: %w", dir, err)
	}
	for _, file := range []struct{ template, name string }{
		{"implementation-cargo.tmpl", ImplementationManifest},
		{"implementation-lib.tmpl", ImplementationLib},
	} {
		if _, err := codegen.GenerateFileIfNotExists(generateFile, file.template, filepath.Join(dir, file.name), view); err != nil {
			return false, fmt.Errorf("write implementation scaffold: %w", err)
		}
	}
	return true, nil
}

// ImplementationExists reports whether dir holds a Rust crate: a
// Cargo.toml.
func ImplementationExists(dir string) (bool, error) {
	_, err := os.Stat(filepath.Join(dir, ImplementationManifest))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read implementation crate %s: %w", dir, err)
	}
	return true, nil
}
