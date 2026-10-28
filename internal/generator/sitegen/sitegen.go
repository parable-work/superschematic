// Package sitegen is the Site kind's generator (docs/stack-model.md,
// section 8.10, D55). A site's code is the package at the naming file's
// [implementation_paths] site template, a member of the Bun workspace
// (D51). The generator writes the site's typed browser config into it on
// every build, ConfigFile: a module that fetches the site's config,
// ir.SiteConfigPath, which a deploy writes per environment, and returns
// each API the site calls with its public base URL and a client of its
// generated TypeScript SDK at that address. When the package is missing,
// it scaffolds a minimal site there once, type-checked and built with
// `bun build` (D101).
//
// The config module imports each API's SDK: the SDKs validate through
// superscalar, whose browser build a bundler takes through the `browser`
// export condition, so `bun build --target=browser` bundles a site with
// no plugin (D55, amended twice). An API whose config generates no
// TypeScript SDK gets its base URL alone.
package sitegen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sdkgen"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
	ir "github.com/parable-work/superschematic/ir"
)

// Name is the generator's name in the Site kind's pipeline.
const Name = "site"

// ConfigFile is the typed browser config the build writes into a site's
// package, on every build.
const ConfigFile = "config.generated.ts"

// TypeScriptVersion is the TypeScript release a scaffolded site
// type-checks with, the one the API implementations' scaffolds pin.
const TypeScriptVersion = "5.9.3"

// TypecheckScript is the script of a scaffolded site's package.json that
// type-checks the site, and with it the SDKs its config imports, which its
// build runs first (D101).
const TypecheckScript = "typecheck"

// API is one API a site calls, as its typed config names it.
type API struct {
	// Service is the API service's name, its key in the config document.
	Service string

	// Property is its member of the typed Apis: the name in camel case.
	Property string

	// Package is the npm name of the API's TypeScript SDK, which the
	// config imports; empty when the API's config generates none.
	Package string

	// Client is the SDK's root class, `ShopApiSDK`; empty with Package.
	Client string
}

// HasSDK reports whether the API's config generates a TypeScript SDK.
func (a API) HasSDK() bool { return a.Package != "" }

// SDKs reports whether the config of the API service named service
// generates a TypeScript SDK. The generator reads it from the API's
// config; a nil SDKs takes every API to have one.
type SDKs func(service string) bool

// Site is what the generator writes for one Site service.
type Site struct {
	// Service is the Site service's name.
	Service string

	// Package is the npm name of the site's package, which a scaffold
	// writes: `<scope>/<service>-site`.
	Package string

	// Config is the site's config with its defaults filled in.
	Config ir.SiteConfig

	// APIs are the APIs it calls, sorted by service.
	APIs []API
}

// Dependencies are what the site's package depends on for the packages
// the config imports: each SDK, a member of the Bun workspace, in the
// order of APIs. The scaffold's package.json names them, and the build
// warns of one a package it no longer writes lacks (D100).
func (s *Site) Dependencies() []tsutil.Dependency {
	var out []tsutil.Dependency
	for _, pkg := range s.SDKPackages() {
		out = append(out, tsutil.Dependency{Name: pkg, Spec: tsutil.WorkspaceSpec})
	}
	return out
}

// SDKPackages are the npm names of the SDKs the config imports, in the
// order of APIs.
func (s *Site) SDKPackages() []string {
	var out []string
	for _, api := range s.APIs {
		if api.HasSDK() {
			out = append(out, api.Package)
		}
	}
	return out
}

// Of reads what the generator writes for the Site schema s, with an SDK
// client for each API it calls whose config generates a TypeScript SDK.
func Of(s *ir.Schema, n naming.Naming, sdks SDKs) (*Site, error) {
	if s == nil || s.Kind != ir.SchemaKindSite {
		return nil, errors.New("sitegen: not a Site schema")
	}
	cfg := ir.SiteConfig{}
	if s.Site != nil {
		cfg = *s.Site
	}
	if err := cfg.Check(); err != nil {
		return nil, fmt.Errorf("sitegen: site %s: %w", s.Name, err)
	}
	site := &Site{
		Service: s.Name,
		Package: PackageName(n, s.Name),
		Config:  cfg.WithDefaults(),
	}
	seen := map[string]string{}
	for _, call := range s.Calls {
		property := tsutil.ToCamelCase(call.Name)
		if other, dup := seen[property]; dup {
			return nil, fmt.Errorf("sitegen: site %s calls %s and %s, which its typed config would both name %s", s.Name, other, call.Name, property)
		}
		seen[property] = call.Name
		api := API{Service: call.Name, Property: property}
		if sdks == nil || sdks(call.Name) {
			api.Package = n.OrDefault().NpmSDKPackage(call.Name)
			api.Client = sdkgen.ClassName(call.Name)
		}
		site.APIs = append(site.APIs, api)
	}
	sort.Slice(site.APIs, func(i, j int) bool { return site.APIs[i].Service < site.APIs[j].Service })
	return site, nil
}

// BuildScript is the command of the scaffold's build script (D101): the
// typecheck script, which type-checks the site and the SDKs it calls, then
// a bundle of index.html and its script with `bun build` into the output.
// Bun's bundler checks no type, so without the first a call the SDK's
// TypeScript refuses reaches the browser.
func (s *Site) BuildScript() string {
	return "bun run " + TypecheckScript + " && " + s.bundleCommand()
}

// bundleCommand bundles index.html and its script with `bun build` into a
// fresh output: the scaffold's whole build script before D101.
func (s *Site) bundleCommand() string {
	return fmt.Sprintf("rm -rf %s && bun build ./index.html --outdir=%s --public-path=/ --minify", s.Config.Output, s.Config.Output)
}

// UncheckedBuild reports whether the package at dir builds the site with
// the build script its scaffold wrote before D101, word for word, which
// bundles without a type-check. A package without a package.json, or
// whose build script is any other, reports false: the package is its
// author's, who may build it as they choose.
func (s *Site) UncheckedBuild(dir string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return false, fmt.Errorf("parse %s: %w", filepath.Join(dir, "package.json"), err)
	}
	return manifest.Scripts[s.Config.Build] == s.bundleCommand(), nil
}

// PackageName is the npm name of a site's package: the scope, the
// service's name and `-site`, apart from the service's own schema package
// of the service's name.
func PackageName(n naming.Naming, service string) string {
	return n.OrDefault().NpmServicePackage(service) + "-site"
}

// Write writes the typed config into dir, the site's package, and
// scaffolds the package when dir does not exist. It reports whether it
// scaffolded.
func (s *Site) Write(dir string) (scaffolded bool, err error) {
	switch _, statErr := os.Stat(dir); {
	case errors.Is(statErr, fs.ErrNotExist):
		if err := s.scaffold(dir); err != nil {
			return false, err
		}
		scaffolded = true
	case statErr != nil:
		return false, statErr
	}
	config, err := s.render(configTemplate)
	if err != nil {
		return scaffolded, err
	}
	return scaffolded, writeIfChanged(filepath.Join(dir, ConfigFile), config)
}

// scaffold writes a minimal site into dir: a package whose build
// type-checks the site, then bundles index.html and its script with `bun
// build` (BuildScript, D101), and whose dev script serves them with Bun's
// dev server, which reloads them on an edit, on PORT, as stack dev runs a
// site's dev server (D82); a script that lists the APIs it calls; and a
// .gitignore for the build's output.
func (s *Site) scaffold(dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		return fmt.Errorf("sitegen: create site package %s: %w", dir, err)
	}
	for _, file := range []struct {
		name     string
		template *template.Template
	}{
		{"package.json", packageTemplate},
		{"tsconfig.json", tsconfigTemplate},
		{"index.html", indexTemplate},
		{filepath.Join("src", "main.ts"), mainTemplate},
		{".gitignore", gitignoreTemplate},
	} {
		content, err := s.render(file.template)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, file.name), content, 0o644); err != nil {
			return fmt.Errorf("sitegen: scaffold %s: %w", file.name, err)
		}
	}
	return nil
}

func (s *Site) render(t *template.Template) ([]byte, error) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, s); err != nil {
		return nil, fmt.Errorf("sitegen: render %s: %w", t.Name(), err)
	}
	return buf.Bytes(), nil
}

// writeIfChanged writes content to path unless it holds it already.
func writeIfChanged(path string, content []byte) error {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, content) {
		return nil
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return fmt.Errorf("sitegen: write %s: %w", path, err)
	}
	return nil
}

var funcs = template.FuncMap{
	"tsQuote":         func(s string) string { return "'" + tsutil.EscapeString(s) + "'" },
	"configPath":      func() string { return ir.SiteConfigPath },
	"tsVersion":       func() string { return TypeScriptVersion },
	"configFile":      func() string { return strings.TrimSuffix(ConfigFile, ".ts") },
	"jsonQuote":       func(s string) string { return fmt.Sprintf("%q", s) },
	"defaultOutput":   func() string { return ir.DefaultSiteOutput },
	"devScript":       func() string { return ir.DefaultSiteDevCommand },
	"typecheckScript": func() string { return TypecheckScript },
}

var configTemplate = template.Must(template.New("config.generated.ts").Funcs(funcs).Parse(`// Code generated by superschematic. DO NOT EDIT.
//
// The typed browser config of site {{ .Service }} (docs/stack-model.md,
// section 8.10, D55). One build of the site serves every environment: the
// deploy writes the site's config per environment, with the public address
// of each API the site calls, at CONFIG_PATH on the site's origin, and
// never caches it. loadApis() reads it once, when the site loads, and
// returns each API with a client of its generated SDK at that address.
//
// The SDKs validate through superscalar, whose browser build a bundler
// takes through the browser export condition (bun build --target=browser,
// with no plugin) and which instantiates its WebAssembly module from the
// bundle: a page's Content-Security-Policy must allow 'wasm-unsafe-eval'
// in script-src (D55, amended).
{{- range .APIs }}{{ if .HasSDK }}
import { {{ .Client }}, type SDKConfig as {{ .Client }}Config } from {{ tsQuote .Package }};
{{- end }}{{ end }}

/** The path of the site's config on its own origin. */
export const CONFIG_PATH = '{{ configPath }}';

/** The site's config, as the deploy writes it. */
export interface SiteConfig {
  /** Each API the site calls, by its service's name. */
  readonly apis: { readonly [service: string]: { readonly url: string } };
}

/** An API the site calls, where a browser reaches it in this environment. */
export interface ApiAddress {
  /** The API service's name. */
  readonly service: string;
  /** The API's public base URL, with no trailing slash. */
  readonly baseUrl: string;
}

/** What a client of an API's SDK takes beside its base URL: auth, credentials, fetch, a timeout. */
export type ClientOptions<Config> = Omit<Config, 'baseUrl'>;

/** An API the site calls, with a client of its generated SDK at its public address. */
export interface ApiEndpoint<Client, Config extends { readonly baseUrl: string }> extends ApiAddress {
  /**
   * Builds a client of the API's SDK at baseUrl, with the SDK's other
   * options: apis.shopApi.client({ auth: { token } }).
   */
  client(options?: ClientOptions<Config>): Client;
}

/** The APIs site {{ .Service }} calls. */
export interface Apis {
{{- range .APIs }}
{{- if .HasSDK }}
  /** {{ .Service }} */
  readonly {{ .Property }}: ApiEndpoint<{{ .Client }}, {{ .Client }}Config>;
{{- else }}
  /** {{ .Service }}, whose config generates no TypeScript SDK: enable outputs.sdk.typescript for a client. */
  readonly {{ .Property }}: ApiAddress;
{{- end }}
{{- end }}
}

let loaded: Promise<SiteConfig> | undefined;

/**
 * Fetches the site's config from its origin, once, uncached. It rejects
 * when the config cannot be read.
 */
export function loadConfig(fetcher: typeof fetch = globalThis.fetch.bind(globalThis)): Promise<SiteConfig> {
  loaded ??= fetcher(CONFIG_PATH, { cache: 'no-store', headers: { Accept: 'application/json' } }).then(async response => {
    if (!response.ok) {
      throw new Error(` + "`" + `site {{ .Service }}: GET ${CONFIG_PATH} answered ${response.status}` + "`" + `);
    }
    return (await response.json()) as SiteConfig;
  });
  loaded.catch(() => {
    loaded = undefined;
  });
  return loaded;
}

/** The address of service in config. Throws when config names no URL for it. */
function addressOf(config: SiteConfig, service: string): ApiAddress {
  const url = config.apis?.[service]?.url;
  if (typeof url !== 'string' || url === '') {
    throw new Error(` + "`" + `site {{ .Service }}: ${CONFIG_PATH} has no URL for ${service}` + "`" + `);
  }
  return { service, baseUrl: url.replace(/\/+$/, '') };
}
{{- if .SDKPackages }}

/** The API at address, whose client create builds at its base URL. */
function endpoint<Client, Config extends { readonly baseUrl: string }>(
  address: ApiAddress,
  create: (config: Config) => Client
): ApiEndpoint<Client, Config> {
  return { ...address, client: options => create({ ...options, baseUrl: address.baseUrl } as Config) };
}
{{- end }}

/**
 * Returns each API the site calls, read from the site's config. It rejects
 * when the config names no URL for one of them.
 */
export async function loadApis(fetcher?: typeof fetch): Promise<Apis> {
{{- if .APIs }}
  const config = await loadConfig(fetcher);
  return {
{{- range .APIs }}
{{- if .HasSDK }}
    {{ .Property }}: endpoint(addressOf(config, {{ tsQuote .Service }}), (options: {{ .Client }}Config) => new {{ .Client }}(options)),
{{- else }}
    {{ .Property }}: addressOf(config, {{ tsQuote .Service }}),
{{- end }}
{{- end }}
  };
{{- else }}
  await loadConfig(fetcher);
  return {};
{{- end }}
}
`))

var packageTemplate = template.Must(template.New("package.json").Funcs(funcs).Parse(`{
  "name": {{ jsonQuote .Package }},
  "version": "0.0.0",
  "description": "The {{ .Service }} site.",
  "private": true,
  "type": "module",
  "scripts": {
    {{ jsonQuote .Config.Build }}: {{ jsonQuote .BuildScript }},
    {{ jsonQuote devScript }}: "bun --port=${PORT:-3000} ./index.html",
    {{ jsonQuote typecheckScript }}: "tsc --noEmit -p tsconfig.json"
  },
{{- with .Dependencies }}
  "dependencies": {
{{- range $i, $dep := . }}{{ if $i }},{{ end }}
    {{ jsonQuote $dep.Name }}: {{ jsonQuote $dep.Spec }}
{{- end }}
  },
{{- end }}
  "devDependencies": {
    "typescript": "{{ tsVersion }}"
  }
}
`))

var tsconfigTemplate = template.Must(template.New("tsconfig.json").Funcs(funcs).Parse(`{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "types": [],
    "strict": true,
    "noEmit": true,
    "skipLibCheck": true,
    "forceConsistentCasingInFileNames": true
  },
  "include": ["**/*.ts"],
  "exclude": ["node_modules", "{{ .Config.Output }}"]
}
`))

var indexTemplate = template.Must(template.New("index.html").Funcs(funcs).Parse(`<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>{{ .Service }}</title>
  </head>
  <body>
    <main id="app">Loading…</main>
    <script type="module" src="./src/main.ts"></script>
  </body>
</html>
`))

var mainTemplate = template.Must(template.New("main.ts").Funcs(funcs).Parse(`// The {{ .Service }} site. superschematic scaffolded this file once; it is yours
// to change. loadApis() reads the address of each API the site calls from
// the config each environment serves at {{ configPath }}.
import { loadApis } from '../{{ configFile }}';

const app = document.getElementById('app');

loadApis().then(
  apis => {
    if (!app) return;
    app.replaceChildren(
      ...Object.values(apis).map(api => {
        const item = document.createElement('p');
        item.textContent = ` + "`" + `${api.service}: ${api.baseUrl}` + "`" + `;
        return item;
      })
    );
  },
  (error: unknown) => {
    if (app) app.textContent = error instanceof Error ? error.message : String(error);
  }
);
`))

var gitignoreTemplate = template.Must(template.New(".gitignore").Funcs(funcs).Parse(`{{ .Config.Output }}/
`))
