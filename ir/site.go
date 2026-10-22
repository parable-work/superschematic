package ir

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
)

// The static site (D55; docs/stack-model.md, section 8.10). A site is a
// service of the core kind Site: a directory a front-end build writes,
// served as files. Its config names the APIs it calls and how it builds
// (SiteConfig). Each Site service in a stack is a deployable of kind site,
// always exposed, with a site edge to the server of each API it calls,
// which must be exposed too. A site edge derives the API's public address
// alone (SiteEndpoint), since the browser carries its end user's token,
// and each API a site calls answers CORS for the origins of the sites that
// call it, derived into the API's CORS field (CORSField, CORSPolicy).

// SiteConfig is a Site service's `site` config: how its code builds and
// what it serves. Every member may be empty for its default.
type SiteConfig struct {
	// Build is the script of the site's package.json that builds it, run
	// with `bun run` in the site's directory after the workspace's
	// install. Empty is DefaultSiteBuild.
	Build string `json:"build,omitempty" yaml:"build,omitempty"`

	// Output is the directory the build writes, relative to the site's
	// directory and inside it. Empty is DefaultSiteOutput.
	Output string `json:"output,omitempty" yaml:"output,omitempty"`

	// Fallback is the file, relative to Output, served for a path that
	// names no file: `index.html` for a single-page application, whose
	// router reads the path. Empty serves 404 for such a path.
	Fallback string `json:"fallback,omitempty" yaml:"fallback,omitempty"`
}

// The defaults of a SiteConfig.
const (
	// DefaultSiteBuild is the package.json script a site builds with.
	DefaultSiteBuild = "build"

	// DefaultSiteOutput is the directory a site's build writes.
	DefaultSiteOutput = "dist"
)

// SiteConfigPath is the path, on a site's origin, of the config the
// deploy writes per environment and the site reads when it loads: each
// API's public address. It is never cached.
const SiteConfigPath = "/__superschematic/config.json"

// The Cache-Control of a site's files (D55, amended), which the gcp
// publisher writes on each object and `stack dev`'s file server sends.
const (
	// SiteCacheRevalidate is a file a cache may keep but asks the origin
	// about before it serves it again, so a new build shows at once.
	SiteCacheRevalidate = "no-cache"

	// SiteCacheImmutable is a file whose name holds its content's hash:
	// other content comes under another name, so a cache keeps it a year
	// and never asks.
	SiteCacheImmutable = "public, max-age=31536000, immutable"
)

// SiteFileCacheControl returns the Cache-Control of a site's file at rel,
// a slash-separated path relative to the site's output, whose fallback
// (relative to the output too) is fallback: SiteCacheImmutable when the
// file's name holds a content hash (ContentHashedName), unless it is an
// HTML file or the fallback, which the site serves for paths that name no
// file; SiteCacheRevalidate otherwise.
func SiteFileCacheControl(rel, fallback string) string {
	rel = path.Clean("/" + strings.ReplaceAll(rel, `\`, "/"))
	ext := strings.ToLower(path.Ext(rel))
	switch {
	case ext == ".html" || ext == ".htm":
	case fallback != "" && rel == path.Clean("/"+strings.ReplaceAll(fallback, `\`, "/")):
	case ContentHashedName(rel):
		return SiteCacheImmutable
	}
	return SiteCacheRevalidate
}

// minContentHash is the fewest hex digits a name's content hash has: the
// 8 a bundler's shortest hashes carry.
const minContentHash = 8

// ContentHashedName reports whether the base name of a file holds a hash
// of its content, as a bundler names its output (`app.3f9a2c1d.js`,
// `vendor-3f9a2c1d.css`, `3f9a2c1d5e7b.png`): the name before its
// extension, or its last part after a dot or a hyphen, is 8 or more
// lowercase hex digits, at least one a digit and one a letter, since
// digits alone are a date or a number, and letters alone a word. A name
// with no extension holds none. The rule is conservative: a hash in
// another alphabet, such as Bun's or esbuild's (`index-5psy29er.js`),
// does not count, since a name written by hand (`og-1200x630.png`) looks
// as much like one.
func ContentHashedName(name string) bool {
	base := path.Base(strings.ReplaceAll(name, `\`, "/"))
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 {
		return false
	}
	stem := base[:dot]
	if i := strings.LastIndexAny(stem, ".-"); i >= 0 {
		stem = stem[i+1:]
	}
	if len(stem) < minContentHash {
		return false
	}
	var digit, letter bool
	for _, r := range stem {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'a' && r <= 'f':
			letter = true
		default:
			return false
		}
	}
	return digit && letter
}

// WithDefaults returns c with every empty member at its default, and an
// Output and a Fallback cleaned to slash-separated relative paths.
func (c SiteConfig) WithDefaults() SiteConfig {
	if c.Build == "" {
		c.Build = DefaultSiteBuild
	}
	if c.Output == "" {
		c.Output = DefaultSiteOutput
	}
	c.Output = path.Clean(strings.ReplaceAll(c.Output, `\`, "/"))
	if c.Fallback != "" {
		c.Fallback = path.Clean(strings.ReplaceAll(c.Fallback, `\`, "/"))
	}
	return c
}

// Check refuses a build script that is no script name, an output
// directory outside the site's directory or the directory itself, and a
// fallback outside the output.
func (c SiteConfig) Check() error {
	if c.Build != "" && (strings.TrimSpace(c.Build) != c.Build || strings.ContainsAny(c.Build, " \t\n")) {
		return fmt.Errorf("site.build %q is no script name: name a script of the site's package.json", c.Build)
	}
	if err := relativeInside("site.output", c.Output); err != nil {
		return err
	}
	if c.Output != "" && path.Clean(c.Output) == "." {
		return fmt.Errorf("site.output %q is the site's directory itself; name the directory its build writes", c.Output)
	}
	return relativeInside("site.fallback", c.Fallback)
}

// relativeInside refuses a path that is absolute or leaves the directory
// it is relative to.
func relativeInside(key, p string) error {
	if p == "" {
		return nil
	}
	clean := path.Clean(strings.ReplaceAll(p, `\`, "/"))
	if strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%s %q leaves the directory it is relative to", key, p)
	}
	return nil
}

// ResolvedSite is what a site deployable builds and serves in one
// environment (D55): its Site service's config with every default filled
// in, and where its code is.
type ResolvedSite struct {
	// Dir is the site's directory, its implementation path, relative to
	// the repository root and slash-separated (`web/shop-web`): a member of
	// the Bun workspace whose root the output root holds (D51).
	Dir string `json:"dir"`

	// Build is the package.json script that builds it; Output the
	// directory, relative to Dir, the build writes; Fallback the file,
	// relative to Output, a path that names no file is served, or empty.
	Build    string `json:"build"`
	Output   string `json:"output"`
	Fallback string `json:"fallback,omitempty"`
}

// SiteDigestToken is what a site's platform writes, in a string property
// of one of the site's own nodes, where the digest of the files the site
// serves goes: a path prefix such as `/{site-digest}/`. The graph is a
// function of the schemas, so it holds the token; a deploy pins it to the
// hex digest of the files it publishes, as it pins an image to its digest,
// and the provisioner's apply of the node switches the site to them at
// once (D55).
const SiteDigestToken = "{site-digest}"

// ArtifactsDir is the directory, under the directory a provisioner renders
// its program in, where a deploy places the files of each bundle it pins,
// and of each site whose target fetches them, at
// `artifacts/<deployable>/<hex digest>/`, after Render and before every
// Plan, Apply or Destroy (D64). A platform names a file there relative to
// the program, with BundleDigestToken or SiteDigestToken for the digest.
const ArtifactsDir = "artifacts"

// BundleDigestToken is what the platform of a deployable whose artifact is
// a bundle writes, in a string property of one of the deployable's own
// nodes, where the hex digest of the bundle's files goes: the directory
// `artifacts/<deployable>/{bundle-digest}/` its files are read from. A
// deploy pins it as it pins SiteDigestToken, and places the files there
// (D64).
const BundleDigestToken = "{bundle-digest}"

// BundleDigestVariable is the variable a job whose artifact is a bundle
// reads the hex digest of its own bundle from, where its platform puts
// BundleDigestToken, which a deploy pins; and JobRunClass is the class the
// job's bundle exports, through which the platform's job runner runs the
// job once and checks it runs the bundle the deploy manifest records: a
// Workflow's on Cloudflare (D64, amended). The server generator writes both
// into a job's scheduled.ts, and the platform names both.
const (
	BundleDigestVariable = "SUPERSCHEMATIC_BUNDLE"
	JobRunClass          = "JobRun"
)

// BundleFileTokenPrefix begins a BundleFileToken.
const BundleFileTokenPrefix = "{bundle-sha256:"

// BundleFileToken returns the token a platform writes where the hex SHA-256
// of one file of a deployable's bundle goes, `{bundle-sha256:<file>}`, file
// being the slash-separated path of the file in the bundle (`main.js`): a
// hash a provider checks the file's content against. A deploy pins it with
// BundleDigestToken (D64).
func BundleFileToken(file string) string { return BundleFileTokenPrefix + file + "}" }

// BundlePath returns the path, relative to a provisioner's program, of one
// file of a deployable's bundle, the digest a BundleDigestToken:
// `artifacts/<deployable>/{bundle-digest}/<file>` (D64).
func BundlePath(deployable, file string) string {
	return ArtifactsDir + "/" + deployable + "/" + BundleDigestToken + "/" + file
}

// SiteEndpoint is what a site edge's connector derives: the public base
// URL of an API the site calls, where a browser reaches it. It holds no
// credential: the browser carries its end user's token (D55).
type SiteEndpoint struct {
	// URL is the API's public base URL.
	URL any `json:"url"`
}

// checkSiteEndpoint checks a site edge's derived value.
func checkSiteEndpoint(v any) error {
	m, err := members(v, "a site endpoint", "url")
	if err != nil {
		return err
	}
	return requiredString(m, "", "url")
}

// CORSFieldSuffix is what the name of an API's CORS field adds to the
// API's name in upper snake case.
const CORSFieldSuffix = "_CORS"

// CORSField returns the name of the CORS field of the API service named
// service: its name in upper snake case and CORSFieldSuffix (`shop-api` is
// `SHOP_API_CORS`, whose one variable is `SHOP_API_CORS_ORIGINS`). The
// server of an API a site calls holds it (D55).
func CORSField(service string) string {
	return EnvName(service) + CORSFieldSuffix
}

// CORSPolicy is the value of an API's CORS field: the origins of the sites
// that call the API, which its server answers CORS for and no other.
type CORSPolicy struct {
	// Origins are the sites' public origins, `<scheme>://<host>[:<port>]`
	// each, a string or a reference, sorted by site.
	Origins []any `json:"origins"`
}

// CheckCORSPolicy checks the value of a CORS field: an object whose one
// member, origins, is a non-empty list of origins, each a string with a
// scheme and a host and nothing after them, or a reference.
func CheckCORSPolicy(v any) error {
	value, err := jsonForm(v)
	if err != nil {
		return err
	}
	m, err := members(value, "a CORS policy", "origins")
	if err != nil {
		return err
	}
	list, ok := m["origins"].([]any)
	if !ok || len(list) == 0 {
		return errors.New("origins must be a non-empty list of origins")
	}
	for i, item := range list {
		switch item := item.(type) {
		case Output, Parameter, Concat:
		case string:
			if err := CheckOrigin(item); err != nil {
				return fmt.Errorf("origins[%d]: %w", i, err)
			}
		default:
			return fmt.Errorf("origins[%d] is %s; want an origin or a reference", i, describeMember(item))
		}
	}
	return nil
}

// CheckOrigin refuses a string that is no origin a browser sends: an http
// or https URL with a host and no path, query, fragment or user.
func CheckOrigin(origin string) error {
	u, err := url.Parse(origin)
	switch {
	case err != nil:
		return fmt.Errorf("%q is no origin: %v", origin, err)
	case u.Scheme != "http" && u.Scheme != "https":
		return fmt.Errorf("%q is no origin: its scheme is not http or https", origin)
	case u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(origin, "/"):
		return fmt.Errorf("%q is no origin: an origin is <scheme>://<host>[:<port>] and nothing more", origin)
	case strings.Contains(origin, ","):
		return fmt.Errorf("%q is no origin: it holds a comma", origin)
	}
	return nil
}
