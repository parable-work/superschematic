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
