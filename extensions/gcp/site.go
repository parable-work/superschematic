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
// is no-cache: the CDN keeps it, and asks the bucket whether it changed
// before it serves it, since a file of a new build may come under the
// same path as one of the last, and the switch to a new build shows at
// once. The config is no-store: a deploy writes it again for the same
// build when an API's address changes.
const (
	siteFileCache   = "no-cache"
	siteConfigCache = "no-store"
)

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
// parameter. A bucket's name is global, and a project's id is too.
func siteBucket(v values, d ir.ResolvedDeployable) any {
	return join(v.project, "-", d.ResourceName)
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
//   - the load balancer's frontend and DNS records (loadBalancer.frontend),
//     its address in the infrastructure phase.
func lowerSite(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	env := ctx.Environment
	v := valuesOf(env)
	if d.Site == nil {
		return registry.Lowered{}, fmt.Errorf("site %s says nothing of how it builds", d.Name)
	}
	bucket := siteBucket(v, d)
	if err := checkLength("the bucket name of "+d.Name, bucket, 3, 63, renameOf(d)); err != nil {
		return registry.Lowered{}, err
	}
	lb := loadBalancer{v: v, d: d}
	infrastructure := func(res *ir.Resource) *ir.Resource {
		res.Phase = ir.PhaseInfrastructure
		return res
	}
	files := lb.out("backend", "id")
	backends := []*ir.Resource{
		{ID: lb.id("bucket"), Type: TypeBucket, Phase: ir.PhaseInfrastructure, Properties: map[string]any{
			"project":                  v.project,
			"name":                     bucket,
			"location":                 strings.ToUpper(v.region),
			"uniformBucketLevelAccess": true,
			"publicAccessPrevention":   "inherited",
			"forceDestroy":             true,
			"website":                  map[string]any{"mainPageSuffix": "index.html"},
		}},
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
	if fallback := d.Site.Fallback; fallback != "" {
		backends = append(backends, infrastructure(lb.node("anchor", TypeBackendService, "anchor", map[string]any{
			"loadBalancingScheme": "EXTERNAL_MANAGED",
		})))
		rules = append(rules, map[string]any{
			"priority":   2,
			"matchRules": []any{map[string]any{"fullPathMatch": siteReservedPath}},
			"service":    lb.out("anchor", "id"),
		})
		matcher["defaultCustomErrorResponsePolicy"] = map[string]any{
			"errorService": files,
			"errorResponseRules": []any{map[string]any{
				"matchResponseCodes":   []any{"404"},
				"path":                 prefix + fallback,
				"overrideResponseCode": 200,
			}},
		}
	}
	matcher["routeRules"] = append(rules, rewrite(3, map[string]any{"prefixMatch": "/"}, prefix))
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

// connectSite realizes a site edge to a Cloud Run server (D55): the site's
// config holds the server's public address (servicePublicAddress). The
// server needs no grant: it is exposed, and its invoker check is off.
func connectSite(ctx registry.ConnectorContext) (registry.Connected, error) {
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
	where := "gs://" + bucket + "/" + hex + "/"
	switch _, err := cloud.ReadObject(ctx, bucket, hex+"/"+siteMarker); {
	case err == nil:
		logf("files of site %s are in %s", req.Site, where)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	case req.Dir == "":
		return fmt.Errorf("gcp: site %s has no files %s in %s; deploy a build of it, or give the digest of files an earlier deploy published", req.Site, req.Digest, where)
	default:
		n, err := uploadSite(ctx, cloud, bucket, hex, req.Dir)
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
// build's digest does.
func uploadSite(ctx context.Context, cloud Cloud, bucket, hex, dir string) (int, error) {
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
				err := uploadFile(ctx, cloud, bucket, hex, dir, rel)
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

// uploadFile puts one file of a site, rel below dir, in bucket under hex.
func uploadFile(ctx context.Context, cloud Cloud, bucket, hex, dir, rel string) error {
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	return cloud.WriteSiteObject(ctx, bucket, hex+"/"+rel, data, siteContentType(rel), siteFileCache)
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
