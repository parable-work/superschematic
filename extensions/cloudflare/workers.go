package cloudflare

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The Workers platform (section 18.2, D64): a TypeScript server is a
// Cloudflare Worker named after its stack, its environment and the
// deployable (names.go), which runs the bundle its
// package's bundle.ts writes (section 18.5). A Worker is three nodes:
//
//   - the `worker`, in infrastructure, so a callee exists before any
//     caller's version binds to it, with Workers Logs on unless the
//     settings turn them off, workers.dev on only for an exposed server
//     without a domain, and previews off;
//   - a `workerVersion` per rollout, which holds the bundle's files, each a
//     contentFile the deploy places beside the program
//     (ir.BundlePath) with its pinned hash (ir.BundleFileToken), the
//     compatibility date, the CPU limit and placement the settings give,
//     and the server's config as bindings;
//   - a `workersDeployment` sending all traffic to that version.
//
// An exposed server under a domain gets a `workersCustomDomain` for
// `<label>.<domain>` in the exposure step, whose record and certificate
// Cloudflare writes: the platform returns no DNS records (section 18.11).

// Workers runs TypeScript servers as Cloudflare Workers.
const Workers = "cloudflare.workers"

// The resource types of a Worker, pinned in schemas/ at ProviderVersion
// but TypeWorkersRoute, which no platform emits and the policy
// nothing-public-unless-exposed names.
const (
	TypeWorker              = "cloudflare:index/worker:Worker"
	TypeWorkerVersion       = "cloudflare:index/workerVersion:WorkerVersion"
	TypeWorkersDeployment   = "cloudflare:index/workersDeployment:WorkersDeployment"
	TypeWorkersCustomDomain = "cloudflare:index/workersCustomDomain:WorkersCustomDomain"
	TypeWorkersRoute        = "cloudflare:index/workersRoute:WorkersRoute"
)

// The files of a server's bundle, the names its bundle.ts writes and the
// version's modules carry (section 18.5): the main module, its source map,
// and superscalar's WebAssembly module, which the main module imports as
// `./superscalar_wasm_bg.wasm`, the name its workerd build imports it by.
const (
	MainModule = "main.js"
	SourceMap  = "main.js.map"
	WasmModule = "superscalar_wasm_bg.wasm"
)

// BundleModules are the files of a server's bundle, the main module first,
// with the content type each is uploaded as.
var BundleModules = []struct{ Name, ContentType string }{
	{MainModule, "application/javascript+module"},
	{SourceMap, "application/source-map"},
	{WasmModule, "application/wasm"},
}

// CompatibilityDate is the Workers runtime's compatibility date a Worker's
// version names unless its settings name another: the date the runtime
// behaves as of, which the extension moves with its tests.
const CompatibilityDate = "2026-09-15"

// noIdentity is why a server on Workers serves no API over the user model
// (section 18.15).
const noIdentity = "the TypeScript identity runtime hashes passwords with node:crypto's argon2, which a Worker does not have (D64)"

// workersSettings is the schema of a server's settings on Workers (section
// 18.1):
//
//   - logs, Workers Logs, on by default;
//   - compatibilityDate, which CompatibilityDate is by default;
//   - cpuMs, the CPU time a request may take, Cloudflare's default when
//     unset;
//   - placement, `smart` for Smart Placement, which runs the Worker near
//     the backends it calls.
const workersSettings = `{
  "type": "object",
  "properties": {
    "logs": {"type": "boolean"},
    "compatibilityDate": {"type": "string", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},
    "cpuMs": {"type": "integer", "minimum": 1, "maximum": 300000},
    "placement": {"enum": ["smart"]}
  },
  "additionalProperties": false
}`

// workersPlatform is the Workers platform's registration.
func workersPlatform() registry.PlatformSpec {
	return registry.PlatformSpec{
		Name:            Workers,
		Kind:            ir.DeployableServer,
		Languages:       []string{registry.APILanguageTypeScript},
		Artifact:        ir.ArtifactBundle,
		Settings:        json.RawMessage(workersSettings),
		NoIdentity:      noIdentity,
		NameOf:          workerName,
		AddressOf:       workerAddress,
		PublicAddressOf: workerPublicAddress,
		Lower:           lowerServer,
	}
}

// workerName names a deployable's Worker after its stack, its environment
// and itself, `acme-shop-staging-shop-api`, or
// `acme-shop-preview-shop-api-pr123` under a parameter (names.go): one
// account may hold several stacks and environments.
func workerName(ctx registry.PlatformContext) any {
	return accountName(ctx.Environment, ctx.Deployable.Name)
}

// workerNameParts are what a Worker's name joins, which a refusal of a
// long one names.
func workerNameParts(env registry.StackEnvironment, d ir.ResolvedDeployable, what string) []namePart {
	return partsOf(env, what, d.Name)
}

// workerAddress is how an edge reaches a Worker: through a service
// binding to it, whose requests carry a URL whose host is the Worker's name
// and goes nowhere else (section 18.2).
func workerAddress(ctx registry.PlatformContext) any {
	return join("https://", ctx.Deployable.ResourceName)
}

// workerPublicAddress is where a browser reaches an exposed Worker: under
// a domain, `https://<label>.<domain>`, its custom domain, the label the
// deployable's name (hostLabel); without one,
// `https://<name>.<workersSubdomain>.workers.dev`, the Worker's name. An environment whose
// values record no workersSubdomain gives it none, and lowerServer refuses
// it. A Worker that is not exposed has none.
func workerPublicAddress(ctx registry.PlatformContext) any {
	d := ctx.Deployable
	if !d.Exposed {
		return nil
	}
	if domain := ctx.Environment.Domain; domain != "" {
		return join("https://", hostLabel(ctx.Environment, d.Name), ".", domain)
	}
	v := targetValuesOf(ctx.Environment.Values)
	if v.workersSubdomain == "" {
		return nil
	}
	return join("https://", d.ResourceName, ".", v.workersSubdomain, ".workers.dev")
}

// lowerServer lowers a server to its Worker's three nodes (workerNodes),
// whose version runs its bundle (bundleVersion), and, when it is exposed
// under a domain, its custom domain. An exposed server without a domain is
// reached at workers.dev, which needs the account's workers.dev subdomain
// that bootstrap records.
func lowerServer(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	env := ctx.Environment
	v := targetValuesOf(env.Values)
	if err := checkWorkerName("the Worker of server "+d.Name, d.ResourceName, workerNameParts(env, d, "the deployable")); err != nil {
		return registry.Lowered{}, err
	}
	workersDev := d.Exposed && env.Domain == ""
	if workersDev && v.workersSubdomain == "" {
		return registry.Lowered{}, fmt.Errorf("server %s is exposed without a domain, at workers.dev, and environment %s records no workersSubdomain; run stack bootstrap %s, which records the account's workers.dev subdomain beside accountId, or set a domain", d.Name, env.Name, env.Name)
	}
	version, err := bundleVersion(ctx, v)
	if err != nil {
		return registry.Lowered{}, err
	}
	out := registry.Lowered{Resources: workerNodes(d, v, workersDev, settingBool(d.Settings, "logs", true), version)}
	// The Worker waits for each Worker its service bindings name, so a
	// destroy removes it, with its bindings, first (calls.go).
	out.Resources[0].DependsOn = bindingDependencies(d)
	if d.Exposed && env.Domain != "" {
		out.Resources = append(out.Resources, customDomain(d, v, env))
	}
	return out, nil
}

// workerID, versionID, deploymentID and domainID are the IDs of a
// deployable's Worker nodes.
func workerID(deployable string) string     { return deployable + ".worker" }
func versionID(deployable string) string    { return deployable + ".version" }
func deploymentID(deployable string) string { return deployable + ".deployment" }
func domainID(deployable string) string     { return deployable + ".domain" }

// workerNodes returns a deployable's Worker (section 18.2): the worker in
// infrastructure, with Workers Logs as logs says, workers.dev as
// workersDev says, and previews off; a version holding version, the
// properties of what it runs; and a deployment sending all traffic to the
// version. A server's, a job's or a site's Worker is these three nodes.
func workerNodes(d ir.ResolvedDeployable, v accountValues, workersDev, logs bool, version map[string]any) []*ir.Resource {
	worker := workerID(d.Name)
	version["accountId"] = v.accountID
	version["workerId"] = ir.Output{Resource: worker, Name: "id"}
	return []*ir.Resource{
		{ID: worker, Type: TypeWorker, Phase: ir.PhaseInfrastructure, Properties: map[string]any{
			"accountId": v.accountID,
			"name":      d.ResourceName,
			"observability": map[string]any{
				"enabled": logs,
				"logs":    map[string]any{"enabled": logs, "invocationLogs": logs},
			},
			"subdomain": map[string]any{"enabled": workersDev, "previewsEnabled": false},
		}},
		{ID: versionID(d.Name), Type: TypeWorkerVersion, Properties: version},
		{ID: deploymentID(d.Name), Type: TypeWorkersDeployment, Properties: map[string]any{
			"accountId":  v.accountID,
			"scriptName": ir.Output{Resource: worker, Name: "name"},
			"strategy":   "percentage",
			"versions": []any{map[string]any{
				"percentage": 100,
				"versionId":  ir.Output{Resource: versionID(d.Name), Name: "id"},
			}},
		}},
	}
}

// customDomain is an exposed Worker's custom domain, `<label>.<domain>`
// under the environment's domain (hostLabel), in the exposure step
// (section 18.11). Cloudflare finds the zone by the hostname within the
// account, writes its record and issues its certificate.
func customDomain(d ir.ResolvedDeployable, v accountValues, env registry.StackEnvironment) *ir.Resource {
	return &ir.Resource{ID: domainID(d.Name), Type: TypeWorkersCustomDomain, Phase: ir.PhaseExposure, Properties: map[string]any{
		"accountId": v.accountID,
		"hostname":  join(hostLabel(env, d.Name), ".", env.Domain),
		"service":   ir.Output{Resource: workerID(d.Name), Name: "name"},
	}}
}

// bundleVersion returns the version properties of a deployable that runs
// its bundle (section 18.5): the bundle's files as modules, each a
// contentFile under the program's artifacts with its pinned hash, main.js
// the main module; the compatibility date; the CPU limit and placement its
// settings give; and its config as bindings (configBindings).
func bundleVersion(ctx registry.PlatformContext, v accountValues) (map[string]any, error) {
	d := ctx.Deployable
	bindings, err := configBindings(ctx, v)
	if err != nil {
		return nil, err
	}
	modules := make([]any, 0, len(BundleModules))
	for _, m := range BundleModules {
		modules = append(modules, map[string]any{
			"name":          m.Name,
			"contentType":   m.ContentType,
			"contentFile":   ir.BundlePath(d.Name, m.Name),
			"contentSha256": ir.BundleFileToken(m.Name),
		})
	}
	version := map[string]any{
		"mainModule":        MainModule,
		"modules":           modules,
		"compatibilityDate": settingString(d.Settings, "compatibilityDate", CompatibilityDate),
	}
	if len(bindings) > 0 {
		version["bindings"] = bindings
	}
	if n, ok := d.Settings["cpuMs"]; ok {
		version["limits"] = map[string]any{"cpuMs": n}
	}
	if mode, ok := d.Settings["placement"].(string); ok {
		version["placement"] = map[string]any{"mode": mode}
	}
	return version, nil
}

// The binding types a version's config takes (section 18.2).
const (
	bindingPlainText    = "plain_text"
	bindingSecretText   = "secret_text"
	bindingSecretsStore = "secrets_store_secret"
	bindingD1           = "d1"
	bindingService      = "service"
	bindingR2           = "r2_bucket"
)

// configBindings returns a Worker's config as its version's bindings,
// sorted by name (section 18.2):
//
//   - a literal, a parameter, and each variable of a derived field
//     (ir.DerivedVariables) is a plain_text binding named as the variable,
//     which the runtime's stackEnvOf reads as a string;
//   - the variable of a secret member, such as an edge's signing key, is a
//     secret_text binding whose text references the output that holds it,
//     so its value lives in the provisioner's encrypted state and never in
//     the program;
//   - a `Secret<T>` field is a secrets_store_secret binding named after the
//     field, naming the environment's Secrets Store and the secret of the
//     environment (SecretName), which the entrypoint awaits once per
//     isolate;
//   - a derived value that names a binding (resourceBinding) adds it: a D1
//     database, a callee's Worker, an R2 bucket.
//
// A secret binding needs the store bootstrap records as secretsStoreId.
func configBindings(ctx registry.PlatformContext, v accountValues) ([]any, error) {
	d := ctx.Deployable
	env := ctx.Environment
	var out []any
	text := func(typ, name string, value any) {
		out = append(out, map[string]any{"type": typ, "name": name, "text": value})
	}
	for _, b := range d.Bindings {
		switch b.Source {
		case ir.BindingLiteral:
			value, err := literalString(b.Value)
			if err != nil {
				return nil, fmt.Errorf("binding %s: %w", b.Field, err)
			}
			text(bindingPlainText, b.Field, value)
		case ir.BindingParameter:
			text(bindingPlainText, b.Field, ir.Parameter(b.Parameter))
		case ir.BindingSecret:
			if v.secretsStoreID == "" {
				return nil, fmt.Errorf("%s %s reads secret %s from the account's Secrets Store, and environment %s records no secretsStoreId; run stack bootstrap %s, which finds or creates the store and records its id beside accountId", d.Kind, d.Name, b.Secret, env.Name, env.Name)
			}
			out = append(out, map[string]any{
				"type":       bindingSecretsStore,
				"name":       b.Field,
				"storeId":    v.secretsStoreID,
				"secretName": SecretName(env.Stack, env.Name, b.Secret),
			})
		case ir.BindingDerived:
			vars, err := ir.DerivedVariables(b.Field, b.Value)
			if err != nil {
				return nil, fmt.Errorf("binding %s: %w", b.Field, err)
			}
			for _, variable := range vars {
				typ := bindingPlainText
				if variable.Secret {
					typ = bindingSecretText
				}
				text(typ, variable.Name, variable.Value)
			}
			res, err := resourceBinding(b.Field, b.Value)
			if err != nil {
				return nil, err
			}
			if res != nil {
				out = append(out, res)
			}
		default:
			return nil, fmt.Errorf("binding %s has source %q", b.Field, b.Source)
		}
	}
	slices.SortStableFunc(out, func(a, b any) int {
		return strings.Compare(a.(map[string]any)["name"].(string), b.(map[string]any)["name"].(string))
	})
	for i := 1; i < len(out); i++ {
		if name := out[i].(map[string]any)["name"]; name == out[i-1].(map[string]any)["name"] {
			return nil, fmt.Errorf("%s %s has two bindings named %s", d.Kind, d.Name, name)
		}
	}
	return out, nil
}

// resourceBinding returns the binding a derived value names, or nil for a
// value that names none (section 18.2):
//
//   - a database connection on D1, `d1: {binding, database}`, is a d1
//     binding named binding of the database's id;
//   - a service endpoint whose `binding` is `{name, worker}` is a service
//     binding named name to the callee's Worker;
//   - a bucket connection whose `binding` is a name is an r2_bucket binding
//     of that name to the bucket its `name` names.
//
// The connectors of D1 databases, of calls between Workers and of R2
// buckets derive them.
func resourceBinding(field string, value any) (map[string]any, error) {
	m, _ := value.(map[string]any)
	if m == nil {
		return nil, nil
	}
	bad := func(what string) error {
		return fmt.Errorf("binding %s: %s", field, what)
	}
	if d1, ok := m["d1"].(map[string]any); ok {
		name, _ := d1["binding"].(string)
		if name == "" || d1["database"] == nil {
			return nil, bad("a D1 connection names its binding and its database")
		}
		return map[string]any{"type": bindingD1, "name": name, "id": d1["database"]}, nil
	}
	switch binding := m["binding"].(type) {
	case nil:
		return nil, nil
	case map[string]any:
		name, _ := binding["name"].(string)
		if name == "" || binding["worker"] == nil {
			return nil, bad("a service binding names itself and the callee's Worker")
		}
		return map[string]any{"type": bindingService, "name": name, "service": binding["worker"]}, nil
	case string:
		if binding == "" || m["name"] == nil {
			return nil, bad("an R2 binding names itself and the bucket")
		}
		return map[string]any{"type": bindingR2, "name": binding, "bucketName": m["name"]}, nil
	}
	return nil, bad(fmt.Sprintf("binding is %T, neither a service binding nor an R2 binding's name", m["binding"]))
}

// literalString is a literal binding's value as a plain_text binding's
// text: a string as it is, a number or a boolean in its JSON form.
func literalString(v any) (string, error) {
	if s, ok := v.(string); ok {
		return s, nil
	}
	data, err := json.Marshal(v)
	return string(data), err
}

func settingString(settings map[string]any, key, def string) string {
	if s, ok := settings[key].(string); ok {
		return s
	}
	return def
}

func settingBool(settings map[string]any, key string, def bool) bool {
	if b, ok := settings[key].(bool); ok {
		return b
	}
	return def
}
