// Package sitegen is the Site kind's generator (docs/stack-model.md,
// section 8.10, D55). A site's code is the package at the naming file's
// [implementation_paths] site template, a member of the Bun workspace
// (D51). The generator writes the site's typed browser config into it on
// every build, ConfigFile: a module that fetches the site's config,
// ir.SiteConfigPath, which a deploy writes per environment, and returns
// each API the site calls with its public base URL and a factory for a
// client of it. When the package is missing, it scaffolds a minimal site
// there once, built with `bun build`.
//
// The config module imports no SDK: a browser bundle of a generated SDK
// takes superscalar's Node backend today, so a site builds a client of an
// API through the factory, from the SDK when it bundles, else from fetch
// (D55, amended).
package sitegen

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/naming"
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

// API is one API a site calls, as its typed config names it.
type API struct {
	// Service is the API service's name, its key in the config document.
	Service string

	// Property is its member of the typed Apis: the name in camel case.
	Property string
}

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

// Of reads what the generator writes for the Site schema s.
func Of(s *ir.Schema, n naming.Naming) (*Site, error) {
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
		site.APIs = append(site.APIs, API{Service: call.Name, Property: property})
	}
	sort.Slice(site.APIs, func(i, j int) bool { return site.APIs[i].Service < site.APIs[j].Service })
	return site, nil
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

// scaffold writes a minimal site into dir: a package whose build bundles
// index.html and its script with `bun build`, a script that lists the
// APIs it calls, and a .gitignore for the build's output.
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
	"tsQuote":       func(s string) string { return "'" + tsutil.EscapeString(s) + "'" },
	"configPath":    func() string { return ir.SiteConfigPath },
	"tsVersion":     func() string { return TypeScriptVersion },
	"configFile":    func() string { return strings.TrimSuffix(ConfigFile, ".ts") },
	"jsonQuote":     func(s string) string { return fmt.Sprintf("%q", s) },
	"defaultOutput": func() string { return ir.DefaultSiteOutput },
}

var configTemplate = template.Must(template.New("config.generated.ts").Funcs(funcs).Parse(`// Code generated by superschematic. DO NOT EDIT.
//
// The typed browser config of site {{ .Service }} (docs/stack-model.md,
// section 8.10, D55). One build of the site serves every environment: the
// deploy writes the site's config per environment, with the public address
// of each API the site calls, at CONFIG_PATH on the site's origin, and
// never caches it. loadApis() reads it once, when the site loads.

/** The path of the site's config on its own origin. */
export const CONFIG_PATH = '{{ configPath }}';

/** The site's config, as the deploy writes it. */
export interface SiteConfig {
  /** Each API the site calls, by its service's name. */
  readonly apis: { readonly [service: string]: { readonly url: string } };
}

/** An API the site calls, where a browser reaches it in this environment. */
export interface ApiEndpoint {
  /** The API service's name. */
  readonly service: string;
  /** The API's public base URL, with no trailing slash. */
  readonly baseUrl: string;
  /**
   * Builds a client of the API at its base URL: its generated SDK, as
   * endpoint.client(options => new ShopApiSDK({ ...options, auth })), or
   * any client that takes a base URL.
   */
  client<T>(create: (options: { readonly baseUrl: string }) => T): T;
}

/** The APIs site {{ .Service }} calls. */
export interface Apis {
{{- range .APIs }}
  /** {{ .Service }} */
  readonly {{ .Property }}: ApiEndpoint;
{{- end }}
}

/** The services of the APIs the site calls, by their member of Apis. */
const services = {
{{- range .APIs }}
  {{ .Property }}: {{ tsQuote .Service }},
{{- end }}
} as const;

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

/**
 * Returns each API the site calls, read from the site's config. It rejects
 * when the config names no URL for one of them.
 */
export async function loadApis(fetcher?: typeof fetch): Promise<Apis> {
  const config = await loadConfig(fetcher);
  const apis: Record<string, ApiEndpoint> = {};
  for (const [property, service] of Object.entries(services)) {
    const url = config.apis?.[service]?.url;
    if (typeof url !== 'string' || url === '') {
      throw new Error(` + "`" + `site {{ .Service }}: ${CONFIG_PATH} has no URL for ${service}` + "`" + `);
    }
    const baseUrl = url.replace(/\/+$/, '');
    apis[property] = { service, baseUrl, client: create => create({ baseUrl }) };
  }
  return apis as unknown as Apis;
}
`))

var packageTemplate = template.Must(template.New("package.json").Funcs(funcs).Parse(`{
  "name": {{ jsonQuote .Package }},
  "version": "0.0.0",
  "description": "The {{ .Service }} site.",
  "private": true,
  "type": "module",
  "scripts": {
    {{ jsonQuote .Config.Build }}: "rm -rf {{ .Config.Output }} && bun build ./index.html --outdir={{ .Config.Output }} --public-path=/ --minify",
    "typecheck": "tsc --noEmit -p tsconfig.json"
  },
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
