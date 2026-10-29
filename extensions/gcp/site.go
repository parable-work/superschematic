package gcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The site platform (docs/stack-model.md, section 7.2, D55): a site's
// files sit in a Cloud Storage bucket of its own, each build's under the
// hex digest of its files, and a global external Application Load
// Balancer serves them through a backend bucket with Cloud CDN, at the
// site's host under the environment's domain. The URL map rewrites every
// path to the prefix of the files the site serves, so the switch to a new
// build, or back to an earlier one, is one change to the URL map, which
// the deploy pins as it pins a server's image (stackdeploy.PinSites).
//
// A site with an origin (D91) serves the APIs of it on the same load
// balancer: each API's path, `/api/<service>` and every path under it,
// goes to a serverless network endpoint group of its Cloud Run service,
// without Cloud CDN, with the path taken off, so the page, its config and
// its APIs share one host and one certificate, and a cookie session needs
// no CORS, no trusted origin and no shared domain.
//
// A backend bucket reads only objects the public can read: Cloud CDN's own
// account reads a private bucket for signed requests alone, and a
// browser's are not signed. So the bucket grants allUsers object viewer,
// which the policy nothing-public-unless-exposed allows a site, since a
// site is always exposed. An organization policy that forbids public
// buckets (domain restricted sharing, or enforced public access
// prevention) refuses the grant, and the site cannot be served from that
// project.

// The objects of a site's bucket the publisher writes beside a build's
// files, under its prefix (sitePublisher).
const (
	// siteMarker is the object written after the last file, so a
	// prefix that holds it holds every file.
	siteMarker = "__superschematic/files"

	// siteReservedPath is the path the URL map sends to the site's
	// anchor, a backend service with no backends (lowerSite).
	siteReservedPath = "/__superschematic/none"
)

// Cache-Control of a site's objects. Cloud CDN keeps what Cloud Storage
// serves as its Cache-Control says (cacheMode USE_ORIGIN_HEADERS). A file
// whose name holds its content's hash is cached a year, immutable: its
// name has that content in every build, so a cached copy is right whether
// the CDN keys it on the path before the URL map's rewrite or after it.
// Every other file, index.html and the fallback among them, is no-cache:
// the CDN keeps it, and asks the bucket whether it changed before it
// serves it, since a file of a new build may come under the same path as
// one of the last, and the switch to a new build shows at once
// (ir.SiteFileCacheControl, D55, amended). The config is no-store: a
// deploy writes it again for the same build when an API's address
// changes.
const siteConfigCache = "no-store"

// siteUploads is how many files of a site the publisher uploads at once.
const siteUploads = 8

// sitePublicAddress is where a browser reaches a site, its origin in the
// CORS field of each API it calls: `https://` and its host under the
// environment's domain, or with no domain, `http://` and its load
// balancer's address, since a certificate needs a host to be issued for.
// A site has no other address: AddressOf is this too.
func sitePublicAddress(ctx registry.PlatformContext) any {
	d := ctx.Deployable
	if ctx.Environment.Domain != "" {
		return join("https://", d.ResourceName, ".", ctx.Environment.Domain)
	}
	return join("http://", ir.Output{Resource: d.Name + ".address", Name: "address"})
}

// siteBucket names a site's bucket: the project, then the site's resource
// name, `acme-staging-shop-web`, or `acme-staging-shop-web-pr123` under a
// parameter. A bucket's name is global, and a project's id is too. A name
// over a bucket's 63 characters is shortened (D97), and the second result
// says so.
func siteBucket(env registry.StackEnvironment, v values, d ir.ResolvedDeployable) (any, bool) {
	return fitted(env, v.project+"-"+kebab(d.Name), "-", 63)
}

// lowerSite lowers a site to:
//
//   - its bucket, in the environment's region, with uniform access, which
//     destroying the environment empties, and a grant of object viewer
//     to allUsers, which a backend bucket needs (see above); in the
//     infrastructure phase, so the deploy puts the files in it before the
//     site's rollout step;
//   - a backend bucket over it with Cloud CDN, which keeps each object as
//     its Cache-Control says;
//   - its URL map, in the rollout phase: `/` serves the build's
//     index.html, and every other path the build's file there, the path
//     rewritten to the build's prefix, ir.SiteDigestToken until the
//     deploy pins it. A site with a fallback serves it, with 200, for a
//     path that has no file, by the URL map's custom error response for
//     404. A load balancer with backend buckets alone serves no custom
//     error response, so the URL map also sends one reserved path to an
//     anchor, a backend service with no backends, which answers it 503;
//   - for each API the site serves on its origin (D91), the backend of its
//     Cloud Run service and the URL map's rules that send its paths
//     there (lowerOrigin), ahead of the files' rule; the fallback then
//     sits on the files' rules alone;
//   - the load balancer's frontend and DNS records (loadBalancer.frontend),
//     its address in the infrastructure phase: one host, and one
//     certificate for it, for the site and the APIs of its origin.
func lowerSite(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	env := ctx.Environment
	v := valuesOf(env)
	if d.Site == nil {
		return registry.Lowered{}, fmt.Errorf("site %s says nothing of how it builds", d.Name)
	}
	bucket, short := siteBucket(env, v, d)
	if err := checkLength("the bucket name of "+d.Name, bucket, 3, 63, renameOf(d)); err != nil {
		return registry.Lowered{}, err
	}
	bucketProps := map[string]any{
		"project":                  v.project,
		"name":                     bucket,
		"location":                 strings.ToUpper(v.region),
		"uniformBucketLevelAccess": true,
		"publicAccessPrevention":   "inherited",
		"forceDestroy":             true,
		"website":                  map[string]any{"mainPageSuffix": "index.html"},
	}
	if labels := nameLabels(d, short); labels != nil {
		bucketProps["labels"] = labels
	}
	lb := loadBalancer{v: v, d: d}
	infrastructure := func(res *ir.Resource) *ir.Resource {
		res.Phase = ir.PhaseInfrastructure
		return res
	}
	files := lb.out("backend", "id")
	backends := []*ir.Resource{
		{ID: lb.id("bucket"), Type: TypeBucket, Phase: ir.PhaseInfrastructure, Properties: bucketProps},
		{ID: lb.id("public-read"), Type: TypeBucketIAMMember, Phase: ir.PhaseInfrastructure, Properties: map[string]any{
			"bucket": lb.out("bucket", "name"),
			"role":   "roles/storage.objectViewer",
			"member": "allUsers",
		}},
		infrastructure(lb.node("backend", TypeBackendBucket, "backend", map[string]any{
			"bucketName": lb.out("bucket", "name"),
			"enableCdn":  true,
			"cdnPolicy":  map[string]any{"cacheMode": "USE_ORIGIN_HEADERS"},
		})),
	}
	prefix := "/" + ir.SiteDigestToken + "/"
	rewrite := func(priority int, match map[string]any, to string) map[string]any {
		return map[string]any{
			"priority":    priority,
			"matchRules":  []any{match},
			"service":     files,
			"routeAction": map[string]any{"urlRewrite": map[string]any{"pathPrefixRewrite": to}},
		}
	}
	rules := []any{rewrite(1, map[string]any{"fullPathMatch": "/"}, prefix+"index.html")}
	matcher := map[string]any{"name": "files", "defaultService": files}
	var notFound func() map[string]any
	if fallback := d.Site.Fallback; fallback != "" {
		backends = append(backends, infrastructure(lb.node("anchor", TypeBackendService, "anchor", map[string]any{
			"loadBalancingScheme": "EXTERNAL_MANAGED",
		})))
		rules = append(rules, map[string]any{
			"priority":   2,
			"matchRules": []any{map[string]any{"fullPathMatch": siteReservedPath}},
			"service":    lb.out("anchor", "id"),
		})
		notFound = func() map[string]any {
			return map[string]any{
				"errorService": files,
				"errorResponseRules": []any{map[string]any{
					"matchResponseCodes":   []any{"404"},
					"path":                 prefix + fallback,
					"overrideResponseCode": 200,
				}},
			}
		}
	}
	apis, apiRules, err := lowerOrigin(lb, v, d, 3)
	if err != nil {
		return registry.Lowered{}, err
	}
	backends = append(backends, apis...)
	rules = append(rules, apiRules...)
	rules = append(rules, rewrite(3+len(apiRules), map[string]any{"prefixMatch": "/"}, prefix))
	switch {
	case notFound == nil:
	case len(apiRules) == 0:
		matcher["defaultCustomErrorResponsePolicy"] = notFound()
	default:
		// The path matcher's policy would answer an API's 404 with the
		// fallback too, so the files' two rules take it alone, and an API's
		// 404 reaches the page as the API answered it (D91).
		rules[0].(map[string]any)["customErrorResponsePolicy"] = notFound()
		rules[len(rules)-1].(map[string]any)["customErrorResponsePolicy"] = notFound()
	}
	matcher["routeRules"] = rules
	urlMap := lb.node("url-map", TypeURLMap, "lb", map[string]any{
		"defaultService": files,
		"hostRules":      []any{map[string]any{"hosts": []any{"*"}, "pathMatcher": "files"}},
		"pathMatchers":   []any{matcher},
	})
	urlMap.Phase = ir.PhaseRollout
	backends = append(backends, urlMap)
	nodes, records := lb.frontend(env, backends)
	// With no domain, the address is the site's origin, which the CORS
	// field of each API it calls lists, so it is there before they roll
	// out.
	for _, res := range nodes {
		if res.ID == lb.id("address") {
			res.Phase = ir.PhaseInfrastructure
		}
	}
	return registry.Lowered{Resources: nodes, Records: records}, nil
}

// originPrefixHeader is the request header that names the path a site's
// origin serves an API under (D91), as `stack dev`'s proxy sets it (D82).
const originPrefixHeader = "X-Forwarded-Prefix"

// lowerOrigin returns the nodes and route rules of the APIs a site serves
// on its origin (D91), the rules' priorities from first, before the rule
// that serves the site's files for every other path. For each API:
//
//   - a serverless network endpoint group of its Cloud Run service, and a
//     backend service over it, without Cloud CDN, so no answer of an API
//     is cached; both in the site's rollout step, after the server's;
//   - two rules, one for the API's path and one for every path under it,
//     that send the request there with the path taken off, its query
//     kept, and X-Forwarded-Prefix the path, as `stack dev`'s proxy
//     forwards it. The load balancer passes the browser's Host and sets
//     X-Forwarded-Proto, so the identity runtime's cross-origin check sees
//     the page's origin.
//
// An API whose server is on another provider, which the generic connector
// reaches at its public address (ir.SiteEndpoint.Upstream), is refused: a
// serverless network endpoint group reaches Cloud Run alone.
func lowerOrigin(lb loadBalancer, v values, d ir.ResolvedDeployable, first int) ([]*ir.Resource, []any, error) {
	var nodes []*ir.Resource
	var rules []any
	for _, api := range d.Site.Origin {
		for _, b := range d.Bindings {
			if value, _ := b.Value.(map[string]any); b.Field == api.Service && value["upstream"] != nil {
				return nil, nil, registry.Unrealizable("site %s serves %s on its origin, and %s's server %s is on another provider; the site's load balancer reaches an API of its origin through a serverless network endpoint group of its Cloud Run service, so place %s on Cloud Run or leave it out of the origin", d.Name, api.Service, api.Service, api.Server, api.Server)
			}
		}
		// The nodes are named after the site and the API; a name over 63
		// characters keeps the site's part and shortens the API's (D97),
		// and the node's description names both.
		service, short := fit(join(d.ResourceName, "-", kebab(api.Service), "-neg"), kebab(api.Service), "-", 63)
		for _, suffix := range []string{"-neg", "-api"} {
			if err := checkLength("the load balancer's "+suffix[1:]+" of "+api.Service+" on site "+d.Name, join(d.ResourceName, "-", service, suffix), 1, 63, "the site or the API"); err != nil {
				return nil, nil, err
			}
		}
		described := func(props map[string]any) map[string]any {
			if short {
				props["description"] = fmt.Sprintf("API %s on the origin of site %s", api.Service, d.Name)
			}
			return props
		}
		part := "api." + api.Service + "."
		group := lb.node(part+"endpoint-group", TypeEndpointGroup, service+"-neg", described(map[string]any{
			"region":              v.region,
			"networkEndpointType": "SERVERLESS",
			"cloudRun":            map[string]any{"service": ir.Output{Resource: api.Server + ".service", Name: "name"}},
		}))
		backend := lb.node(part+"backend", TypeBackendService, service+"-api", described(map[string]any{
			"loadBalancingScheme": "EXTERNAL_MANAGED",
			"backends":            []any{map[string]any{"group": lb.out(part+"endpoint-group", "id")}},
			"enableCdn":           false,
		}))
		group.Phase, backend.Phase = ir.PhaseRollout, ir.PhaseRollout
		nodes = append(nodes, group, backend)
		route := func(priority int, match map[string]any) map[string]any {
			return map[string]any{
				"priority":    priority,
				"matchRules":  []any{match},
				"service":     lb.out(part+"backend", "id"),
				"routeAction": map[string]any{"urlRewrite": map[string]any{"pathPrefixRewrite": "/"}},
				"headerAction": map[string]any{"requestHeadersToAdds": []any{map[string]any{
					"headerName": originPrefixHeader, "headerValue": api.Path, "replace": true,
				}}},
			}
		}
		rules = append(rules, route(first, map[string]any{"fullPathMatch": api.Path}), route(first+1, map[string]any{"prefixMatch": api.Path + "/"}))
		first += 2
	}
	return nodes, rules, nil
}

// connectSite realizes a site edge to a Cloud Run server (D55): the site's
// config holds the server's public address (servicePublicAddress). The
// server needs no grant: it is exposed, and its invoker check is off. For
// an API the site serves on its origin (D91), the config holds the API's
// path on the site's own origin (ir.SiteOriginURL), which the site's load
// balancer routes to the server (lowerOrigin), exposed or not.
func connectSite(ctx registry.ConnectorContext) (registry.Connected, error) {
	if ctx.From.Site.OriginAPI(ctx.Edge.Service.Name) != nil {
		return registry.Connected{Value: ir.SiteEndpoint{URL: ir.SiteOriginURL(ctx.From.PublicAddress, ctx.Edge.Service.Name)}}, nil
	}
	return registry.Connected{Value: ir.SiteEndpoint{URL: ctx.To.PublicAddress}}, nil
}

// sitePublisher is the gcp target's SitePublisher (D55). It puts a build's
// files in the site's bucket under the build's hex digest, as their paths
// below the build's directory, each with its content type and
// Cache-Control, and siteMarker last; files whose marker is there are not
// put again. It writes the run's config at ir.SiteConfigPath under the
// same prefix, where the URL map serves it once the deploy switches to the
// build. The files of every build stay, so a deploy given an earlier
// digest switches back to them.
type sitePublisher struct{ ext Extension }

var _ registry.SitePublisher = sitePublisher{}

// Publish uploads req's files unless they are in the site's bucket, and
// writes its config.
func (p sitePublisher) Publish(ctx context.Context, req registry.SitePublishRequest) error {
	if err := req.Check(); err != nil {
		return err
	}
	env := req.Run.Environment
	if d := env.Deployable(req.Site); d.Platform != Site {
		return fmt.Errorf("gcp: site %s is on platform %s, not %s, so the gcp target does not publish it", req.Site, d.Platform, Site)
	}
	bucket, err := nodeString(env, req.Site+".bucket", "name", req.Run.Parameters)
	if err != nil {
		return err
	}
	log := req.Log
	if log == nil {
		log = io.Discard
	}
	logf := func(format string, args ...any) { _, _ = fmt.Fprintf(log, "  "+format+"\n", args...) }
	cloud := p.ext.cloud()
	hex := strings.TrimPrefix(req.Digest, "sha256:")
	fallback := ""
	if d := env.Deployable(req.Site); d.Site != nil {
		fallback = d.Site.Fallback
	}
	where := "gs://" + bucket + "/" + hex + "/"
	switch _, err := cloud.ReadObject(ctx, bucket, hex+"/"+siteMarker); {
	case err == nil:
		logf("files of site %s are in %s", req.Site, where)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	case req.Dir == "":
		return fmt.Errorf("gcp: site %s has no files %s in %s; deploy a build of it, or give the digest of files an earlier deploy published", req.Site, req.Digest, where)
	default:
		n, err := uploadSite(ctx, cloud, bucket, hex, req.Dir, fallback)
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("gcp: site %s: %w; the infrastructure step of a deploy of %s creates its bucket", req.Site, err, req.Run.Name())
		}
		if err != nil {
			return err
		}
		if err := cloud.WriteSiteObject(ctx, bucket, hex+"/"+siteMarker, []byte(req.Digest+"\n"), "text/plain; charset=utf-8", siteConfigCache); err != nil {
			return err
		}
		logf("uploaded %d files of site %s to %s", n, req.Site, where)
	}
	if req.Config == nil {
		return nil
	}
	if err := cloud.WriteSiteObject(ctx, bucket, hex+ir.SiteConfigPath, req.Config, "application/json", siteConfigCache); err != nil {
		return err
	}
	logf("wrote the config of %s for %s to %s", req.Site, req.Run.Name(), where+strings.TrimPrefix(ir.SiteConfigPath, "/"))
	return nil
}

// uploadSite puts every file below dir in bucket under hex, siteUploads
// at a time, and returns how many. It refuses a symbolic link, as the
// build's digest does. fallback is the site's, relative to dir.
func uploadSite(ctx context.Context, cloud Cloud, bucket, hex, dir, fallback string) (int, error) {
	var files []string
	err := filepath.WalkDir(dir, func(p string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case entry.Type()&fs.ModeSymlink != 0:
			return fmt.Errorf("gcp: %s is a symbolic link; a site's build writes files", p)
		case entry.IsDir():
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return 0, err
	}
	var (
		mu    sync.Mutex
		first error
		wg    sync.WaitGroup
	)
	work := make(chan string)
	for range siteUploads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rel := range work {
				err := uploadFile(ctx, cloud, bucket, hex, dir, rel, fallback)
				mu.Lock()
				if err != nil && first == nil {
					first = err
				}
				mu.Unlock()
			}
		}()
	}
	for _, rel := range files {
		work <- rel
	}
	close(work)
	wg.Wait()
	return len(files), first
}

// uploadFile puts one file of a site, rel below dir, in bucket under hex,
// with the Cache-Control its name and the site's fallback give it.
func uploadFile(ctx context.Context, cloud Cloud, bucket, hex, dir, rel, fallback string) error {
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	return cloud.WriteSiteObject(ctx, bucket, hex+"/"+rel, data, siteContentType(rel), ir.SiteFileCacheControl(rel, fallback))
}

// siteContentType is the content type a file of a site is served with,
// by its extension; application/octet-stream when the extension says
// nothing.
func siteContentType(name string) string {
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}
