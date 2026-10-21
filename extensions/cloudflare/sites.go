package cloudflare

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The site platform (section 18.8, D55, D64): a static site is a Worker,
// named as a server's is (workerName), that serves the site's build as static
// assets. Its version holds:
//
//   - the build's files as its assets' directory, the program's
//     `artifacts/<site>/{site-digest}`, which the deploy pins to the files'
//     digest and fills from the build or the site store (siteStore, an
//     ArtifactFetcher), as it places a bundle's files;
//   - its assets' config: htmlHandling none, so a path names a file
//     exactly, notFoundHandling none, so a path that names no file runs
//     the script, and the script first for every path under
//     /__superschematic/, so the config's path is the script's even when a
//     build holds a file there;
//   - the script the extension embeds (site.js), the same bytes in every
//     release, which answers the config, a directory's index.html and the
//     fallback;
//   - an assets binding, ASSETS, through which the script reads the files;
//   - SITE_CONFIG, a plain_text binding holding the config of section
//     8.10, each API the site calls with its public address, which the
//     target composes from names alone (workerPublicAddress), so the graph
//     carries it and no publish writes it; and SITE_FALLBACK, the fallback's
//     path in the build, when the site has one.
//
// A site is always exposed: under a domain at a custom domain,
// `<label>.<domain>`, in the exposure step, else at workers.dev, which needs
// the subdomain bootstrap records. Its public address is its origin, which
// the CORS field of each API it calls lists.

// Sites serves static sites as Workers with static assets.
const Sites = "cloudflare.sites"

// SiteConnector derives a site edge to a server on Workers: the server's
// public address, which the site's config names.
const SiteConnector = "cloudflare.site-workers"

// The script module of a site's Worker, and the bindings it reads.
const (
	SiteModule          = "site.js"
	SiteAssetsBinding   = "ASSETS"
	SiteConfigBinding   = "SITE_CONFIG"
	SiteFallbackBinding = "SITE_FALLBACK"
)

// The assets' config of a site's version (section 18.8).
const (
	siteHTMLHandling     = "none"
	siteNotFoundHandling = "none"

	// siteWorkerFirst is the paths the script answers before any file:
	// superschematic's own, the config's among them.
	siteWorkerFirst = "/__superschematic/*"
)

// bindingAssets is the binding type of a Worker's static assets.
const bindingAssets = "assets"

//go:embed site.js
var siteScript string

// SiteScript returns the script every site's Worker runs: an ES module
// whose fetch handler answers a path that names no file of the site's
// build (section 18.8).
func SiteScript() string { return siteScript }

// sitesPlatform is the site platform's registration. A site's address,
// the one AddressOf gives, is its public address: nothing else reaches it.
func sitesPlatform() registry.PlatformSpec {
	return registry.PlatformSpec{
		Name:            Sites,
		Kind:            ir.DeployableSite,
		NameOf:          workerName,
		AddressOf:       workerPublicAddress,
		PublicAddressOf: workerPublicAddress,
		Lower:           lowerSite,
	}
}

// lowerSite lowers a site to its Worker's three nodes, workers.dev on
// without a domain, whose version serves the site's files with the
// embedded script, and under a domain its custom domain. A site without a
// domain needs the account's workers.dev subdomain, which bootstrap
// records.
func lowerSite(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	env := ctx.Environment
	v := targetValuesOf(env.Values)
	if d.Site == nil {
		return registry.Lowered{}, fmt.Errorf("site %s says nothing of how it builds", d.Name)
	}
	if err := checkWorkerName("the Worker of site "+d.Name, d.ResourceName, workerNameParts(env, d, "the site")); err != nil {
		return registry.Lowered{}, err
	}
	workersDev := env.Domain == ""
	if workersDev && v.workersSubdomain == "" {
		return registry.Lowered{}, fmt.Errorf("site %s is served without a domain, at workers.dev, and environment %s records no workersSubdomain; run stack bootstrap %s, which records the account's workers.dev subdomain beside accountId, or set a domain", d.Name, env.Name, env.Name)
	}
	config, err := siteConfig(d)
	if err != nil {
		return registry.Lowered{}, err
	}
	bindings := []any{
		map[string]any{"type": bindingAssets, "name": SiteAssetsBinding},
		map[string]any{"type": bindingPlainText, "name": SiteConfigBinding, "text": config},
	}
	if fallback := d.Site.Fallback; fallback != "" {
		bindings = append(bindings, map[string]any{"type": bindingPlainText, "name": SiteFallbackBinding, "text": strings.TrimPrefix(path.Clean("/"+fallback), "/")})
	}
	version := map[string]any{
		"mainModule": SiteModule,
		"modules": []any{map[string]any{
			"name":          SiteModule,
			"contentType":   "application/javascript+module",
			"contentBase64": base64.StdEncoding.EncodeToString([]byte(siteScript)),
		}},
		"compatibilityDate": CompatibilityDate,
		"assets": map[string]any{
			"directory": ir.ArtifactsDir + "/" + d.Name + "/" + ir.SiteDigestToken,
			"config": map[string]any{
				"htmlHandling":     siteHTMLHandling,
				"notFoundHandling": siteNotFoundHandling,
				"runWorkerFirst":   []any{siteWorkerFirst},
			},
		},
		"bindings": bindings,
	}
	out := registry.Lowered{Resources: workerNodes(d, v, workersDev, true, version)}
	if env.Domain != "" {
		out.Resources = append(out.Resources, customDomain(d, v, env))
	}
	return out, nil
}

// siteConfig is the text of a site's SITE_CONFIG binding: the JSON of
// section 8.10, `{"apis":{"<API>":{"url":"<public address>"}}}`, the APIs
// by name, as the deploy writes it for a site on another target
// (stackdeploy.SiteConfigOf). Each address is composed from names alone, a
// string or a concatenation with the environment's parameters, whose
// values are JSON-safe in a string; one that references an output, which
// the graph knows only after the apply, is refused.
func siteConfig(d ir.ResolvedDeployable) (any, error) {
	bindings := slices.Clone(d.Bindings)
	slices.SortFunc(bindings, func(a, b *ir.Binding) int { return strings.Compare(a.Field, b.Field) })
	parts := []any{`{"apis":{`}
	for i, b := range bindings {
		if i > 0 {
			parts = append(parts, ",")
		}
		key, err := json.Marshal(b.Field)
		if err != nil {
			return nil, err
		}
		parts = append(parts, string(key)+`:{"url":"`)
		url, err := siteURL(b.Value)
		if err != nil {
			return nil, fmt.Errorf("site %s: the address of %s %w", d.Name, b.Field, err)
		}
		pieces := []any{url}
		if c, ok := url.(ir.Concat); ok {
			pieces = c
		}
		for _, piece := range pieces {
			switch piece := piece.(type) {
			case string:
				quoted, err := json.Marshal(piece)
				if err != nil {
					return nil, err
				}
				parts = append(parts, string(quoted[1:len(quoted)-1]))
			case ir.Parameter:
				parts = append(parts, piece)
			default:
				return nil, fmt.Errorf("site %s: the address of %s references %v, which the graph knows only once it applied; a site's config on Workers is composed from names alone", d.Name, b.Field, piece)
			}
		}
		parts = append(parts, `"}`)
	}
	parts = append(parts, "}}")
	return join(parts...), nil
}

// siteURL is the URL a site edge's derived value gives.
func siteURL(value any) (any, error) {
	switch v := value.(type) {
	case ir.SiteEndpoint:
		return v.URL, nil
	case *ir.SiteEndpoint:
		return v.URL, nil
	case map[string]any:
		if url, ok := v["url"]; ok && url != nil {
			return url, nil
		}
	}
	return nil, fmt.Errorf("is %v, not a site endpoint with a url", value)
}

// connectSite realizes a site edge to a server on Workers (D55): the
// site's config holds the server's public address. The server needs no
// grant: it is exposed, and a browser carries its end user's token.
func connectSite(ctx registry.ConnectorContext) (registry.Connected, error) {
	return registry.Connected{Value: ir.SiteEndpoint{URL: ctx.To.PublicAddress}}, nil
}

// The site store (section 18.8, D64): the files of each site build a
// deploy or `stack build` publishes sit in the state bucket under
// SitePrefix, at `<stack>/<site>/<hex>/<file>`, `<hex>` being the digest's,
// each with its SHA-256 and mode, and a marker written after the last
// file, as the bundle store keeps a bundle's (putFiles). Fetch writes them
// back for the deploy to place beside the program, where the provider
// uploads them as the Worker's assets, so a plan in CI, a `--site`
// rollback and a `--no-build` deploy serve the files the digest names. The
// store writes no config: the graph carries it, in SITE_CONFIG. Nothing is
// ever deleted: the files of every digest stay for a rollback.
type siteStore struct{ ext Extension }

var (
	_ registry.SitePublisher   = siteStore{}
	_ registry.ArtifactFetcher = siteStore{}
)

// SiteObjects is the prefix in StateBucket that holds the files of a site's
// build under a digest, `sha256:<hex>`:
// `superschematic/sites/<stack>/<site>/<hex>/`.
func SiteObjects(stack, site, digest string) string {
	return SitePrefix + stack + "/" + site + "/" + strings.TrimPrefix(digest, "sha256:") + "/"
}

// Publish keeps the files of req.Dir under req.Digest, unless the marker
// says they are there. With no Dir, it refuses a digest whose files are not
// there. It writes no config.
func (s siteStore) Publish(ctx context.Context, req registry.SitePublishRequest) error {
	if err := req.Check(); err != nil {
		return err
	}
	env := req.Run.Environment
	if d := env.Deployable(req.Site); d.Platform != Sites {
		return fmt.Errorf("cloudflare: site %s is on platform %s, not %s, so the cloudflare target does not publish it", req.Site, d.Platform, Sites)
	}
	cloud, account, err := bundleStore(s).open(req.Run)
	if err != nil {
		return err
	}
	log := req.Log
	if log == nil {
		log = io.Discard
	}
	prefix := SiteObjects(env.Stack, req.Site, req.Digest)
	where := "r2://" + StateBucket + "/" + prefix
	switch _, _, err := cloud.GetObject(ctx, account, StateBucket, prefix+filesMarker); {
	case err == nil:
		_, _ = fmt.Fprintf(log, "  files of site %s are in %s\n", req.Site, where)
		return nil
	case !errors.Is(err, fs.ErrNotExist):
		return err
	case req.Dir == "":
		return fmt.Errorf("cloudflare: site %s has no files %s in %s; deploy a build of it, or give the digest of files an earlier deploy or build published", req.Site, req.Digest, where)
	}
	if err := putFiles(ctx, cloud, account, prefix, req.Dir, req.Digest); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(log, "  kept the files of site %s in %s\n", req.Site, where)
	return nil
}

// Fetch writes the files kept for a site's build under digest into dest,
// each checked against the SHA-256 it was published with.
func (s siteStore) Fetch(ctx context.Context, run registry.Run, site, digest, dest string) error {
	cloud, account, err := bundleStore(s).open(run)
	if err != nil {
		return err
	}
	return getFiles(ctx, cloud, account, SiteObjects(run.Environment.Stack, site, digest), digest, dest)
}
