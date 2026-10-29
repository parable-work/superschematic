package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The local target's sites (D55; docs/stack-model.md, section 8.3). A
// site's node names its package under the repository root, the script
// that builds it and the directory the build writes. At its rollout wave,
// once the servers it calls are ready, the provisioner installs the Bun
// workspace, runs the build once, in the package, or in an output root
// other than the default one in the site's mirror there (tsgen.SiteDir,
// D65, amended), and serves the directory from a file
// server of its own on loopback: each file as it is, the site's config at
// ir.SiteConfigPath, and the single-page fallback for a path that names no
// file. The config and every HTML file are served uncached, so a reload
// sees a rebuilt site.
//
// A site whose node names a dev server (D82) is not built: the provisioner
// runs the dev server's command in the package, with its port in PORT,
// waits until it answers, and serves the site's port from SiteProxy, which
// puts the dev server and the APIs the site calls on the site's one
// origin. The dev server is a process of the environment as a server is:
// one that exits stops the environment, and stopping the environment stops
// it.
//
// A site with an origin (D91) is served from OriginProxy whether it is
// built or runs a dev server: the APIs of its origin on its one origin, as
// a deploy serves them, and every other API it calls at its own address,
// which answers CORS for the site.

// Site is a site node: the package the provisioner builds and the file
// server it serves the build's output from.
type Site struct {
	ID         string `json:"id"`
	Deployable string `json:"deployable"`
	Name       string `json:"name"`
	Wave       int    `json:"wave"`

	// Dir is the site's package, relative to the repository root and
	// slash-separated; Build the package.json script that builds it;
	// Output the directory, relative to Dir, it writes; Fallback the file,
	// relative to Output, served for a path that names no file.
	Dir      string `json:"dir"`
	Build    string `json:"build"`
	Output   string `json:"output"`
	Fallback string `json:"fallback,omitempty"`

	Port int    `json:"port"`
	URL  string `json:"url"`

	// Config is the site's config, served at ir.SiteConfigPath.
	Config map[string]any `json:"config"`

	// Dev is the site's dev server, which runs in place of the build;
	// nil builds the site (D82).
	Dev *SiteDev `json:"dev,omitempty"`

	// Origin is the loopback URL of each API the site serves on its own
	// origin (D91), by service: its proxy serves each under
	// ir.SiteDevAPIPath(service), in front of the dev server or the
	// build. Empty serves none, and a dev server's proxy serves every API
	// the site calls, as D82 has it.
	Origin map[string]string `json:"origin,omitempty"`
}

// SiteDev is a site's dev server (D82): the package.json script, or
// binary, `bun run` runs in the site's package, and the port it listens on.
type SiteDev struct {
	Command string `json:"command"`
	Port    int    `json:"port"`
}

// URL is where the proxy reaches the dev server: on localhost, which a
// dev server that listens on localhost binds to IPv6 alone as often as to
// IPv4, and which the proxy's dial tries both of.
func (d *SiteDev) URL() string { return "http://localhost:" + strconv.Itoa(d.Port) }

func siteOf(res *ir.Resource) (*Site, error) {
	s := &Site{ID: res.ID, Deployable: strings.TrimSuffix(res.ID, ".site")}
	if len(res.Owners) == 1 {
		s.Deployable = res.Owners[0]
	}
	var ok bool
	for _, member := range []struct {
		key string
		to  *string
	}{{"name", &s.Name}, {"dir", &s.Dir}, {"build", &s.Build}, {"output", &s.Output}} {
		if *member.to, ok = res.Properties[member.key].(string); !ok || *member.to == "" {
			return nil, fmt.Errorf("its %s is not a string", member.key)
		}
	}
	if fallback, set := res.Properties["fallback"]; set {
		if s.Fallback, ok = fallback.(string); !ok {
			return nil, fmt.Errorf("its fallback is not a string")
		}
	}
	if s.Port, ok = intValue(res.Properties["port"]); !ok {
		return nil, fmt.Errorf("its port is not a number")
	}
	if s.Config, ok = res.Properties["config"].(map[string]any); !ok {
		return nil, fmt.Errorf("its config is not an object")
	}
	if raw, set := res.Properties["dev"]; set {
		dev, _ := raw.(map[string]any)
		s.Dev = &SiteDev{}
		if s.Dev.Command, ok = dev["command"].(string); !ok || s.Dev.Command == "" {
			return nil, fmt.Errorf("its dev server's command is not a string")
		}
		if s.Dev.Port, ok = intValue(dev["port"]); !ok {
			return nil, fmt.Errorf("its dev server's port is not a number")
		}
	}
	if raw, set := res.Properties["origin"]; set {
		origin, _ := raw.(map[string]any)
		s.Origin = map[string]string{}
		for service, upstream := range origin {
			if s.Origin[service], ok = upstream.(string); !ok {
				return nil, fmt.Errorf("its origin's upstream of %s is not a string", service)
			}
		}
	}
	s.URL = ServerURL(s.Port)
	return s, nil
}

// apis returns the URL of each API the site calls, by service, from its
// config.
func (s *Site) apis() (map[string]*url.URL, error) {
	apis, _ := s.Config["apis"].(map[string]any)
	out := make(map[string]*url.URL, len(apis))
	for service, raw := range apis {
		api, _ := raw.(map[string]any)
		text, _ := api["url"].(string)
		u, err := url.Parse(text)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("site %s: its config holds no URL of %s", s.Deployable, service)
		}
		out[service] = u
	}
	return out, nil
}

// routes returns what the site's proxy serves on its origin: the upstream
// of each API it forwards under its path, and the URL the config gives
// each other API it calls. A site with an origin forwards its origin's
// APIs alone (D91); one without forwards every API it calls, as a dev
// server's proxy does (D82).
func (s *Site) routes() (proxied map[string]*url.URL, direct map[string]string, err error) {
	apis, err := s.apis()
	if err != nil {
		return nil, nil, err
	}
	if len(s.Origin) == 0 {
		return apis, nil, nil
	}
	proxied, direct = map[string]*url.URL{}, map[string]string{}
	for service, api := range apis {
		upstream, ok := s.Origin[service]
		if !ok {
			direct[service] = api.String()
			continue
		}
		u, err := url.Parse(upstream)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, nil, fmt.Errorf("site %s: its origin holds no URL of %s", s.Deployable, service)
		}
		proxied[service] = u
	}
	return proxied, direct, nil
}

func (p *Program) site(id string) *Site {
	for _, s := range p.Sites {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// runningSite is a site's file server, or its dev server and the proxy in
// front of it, which the provisioner started.
type runningSite struct {
	site   *Site
	server *http.Server
	done   chan struct{}

	// proc is the dev server's process and output its writers; nil for a
	// built site (D82).
	proc   Process
	output []*prefixWriter
}

// startSites builds each site once, after one install of the Bun
// workspace, and serves its output on its port, or runs the dev server of
// a site that has one behind its proxy (D82), unless BuildSites is set. A
// site this provisioner serves already is stopped first.
func (p *Provisioner) startSites(ctx context.Context, req registry.ProvisionRequest, sites []*Site) error {
	if req.OutputRoot == "" || req.RepositoryRoot == "" {
		return errors.New("local: the request names no output root, whose Bun workspace each site is a member of, or no repository root, under which each site's package lies")
	}
	bun, err := p.lookPath("bun")
	if err != nil {
		return err
	}
	k := key(req.Environment)
	for _, s := range sites {
		dir, err := tsgen.SiteDir(req.OutputRoot, req.RepositoryRoot, s.Deployable, s.Dir)
		if err != nil {
			return fmt.Errorf("local: %w", err)
		}
		if info, err := os.Stat(filepath.Join(dir, "package.json")); err != nil || info.IsDir() {
			return fmt.Errorf("local: site %s: no package at %s; the site's build scaffolds it (docs/stack-model.md, section 8.10)", s.Deployable, dir)
		}
		if err := p.installWorkspace(ctx, req.OutputRoot, bun); err != nil {
			return err
		}
		if s.Dev != nil && !p.BuildSites {
			if err := p.startDevSite(ctx, k, s, dir, bun); err != nil {
				return err
			}
			continue
		}
		p.printf("build site %s: bun run %s in %s", s.Deployable, s.Build, dir)
		if _, err := p.runner().Run(ctx, Command{Path: bun, Args: []string{"run", s.Build}, Dir: dir, Env: os.Environ()}); err != nil {
			return fmt.Errorf("local: build site %s: %w", s.Deployable, err)
		}
		root := filepath.Join(dir, filepath.FromSlash(s.Output))
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			return fmt.Errorf("local: site %s: its build wrote no directory %s", s.Deployable, root)
		}
		config, err := json.Marshal(s.Config)
		if err != nil {
			return err
		}
		if err := p.stopSite(k, s.ID); err != nil {
			return err
		}
		if p.runner().PortInUse(s.Port) {
			return fmt.Errorf("local: site %s: port %d is in use, by another program or a site an earlier run left behind; stop it, or give %s another port with its port setting", s.Deployable, s.Port, s.Deployable)
		}
		handler := SiteHandler(root, s.Fallback, config)
		var served []string
		if len(s.Origin) > 0 {
			// The build behind the proxy that serves the site's origin
			// (D91), as a deploy serves it.
			proxied, direct, err := s.routes()
			if err != nil {
				return fmt.Errorf("local: %w", err)
			}
			handler = OriginProxy(s.Deployable, handler, proxied, direct)
			served = sortedServices(proxied)
		}
		listener, err := net.Listen("tcp", net.JoinHostPort(Loopback, strconv.Itoa(s.Port)))
		if err != nil {
			return fmt.Errorf("local: site %s: listen on %s: %w", s.Deployable, s.URL, err)
		}
		rs := &runningSite{
			site:   s,
			server: &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second},
			done:   make(chan struct{}),
		}
		go func() {
			defer close(rs.done)
			_ = rs.server.Serve(listener)
		}()
		p.mu.Lock()
		if p.sites == nil {
			p.sites = map[string][]*runningSite{}
		}
		p.sites[k] = append(p.sites[k], rs)
		p.mu.Unlock()
		if len(served) > 0 {
			p.printf("serve site %s at %s from %s, and %s under %s<service>", s.Deployable, s.URL, root, strings.Join(served, ", "), ir.SiteDevAPIPrefix)
			continue
		}
		p.printf("serve site %s at %s from %s", s.Deployable, s.URL, root)
	}
	return nil
}

// sortedServices returns the services of apis, sorted.
func sortedServices(apis map[string]*url.URL) []string {
	services := make([]string, 0, len(apis))
	for service := range apis {
		services = append(services, service)
	}
	sort.Strings(services)
	return services
}

// startDevSite runs a site's dev server in dir, its package, with bun,
// waits until it answers, and serves the site's port from its proxy (D82):
// every API the site calls, or with an origin (D91) the origin's APIs, the
// config naming each other API at its own URL, as a deploy does.
// The dev server takes the provisioner's environment, as the build does,
// with PORT its port, SUPERSCHEMATIC_SITE_URL the site's origin, and, for
// each API the site calls, <API>_URL the API's own loopback URL, for a dev
// server that proxies an API itself.
func (p *Provisioner) startDevSite(ctx context.Context, k string, s *Site, dir, bun string) error {
	apis, direct, err := s.routes()
	if err != nil {
		return fmt.Errorf("local: %w", err)
	}
	dev, err := url.Parse(s.Dev.URL())
	if err != nil {
		return err
	}
	if err := p.stopSite(k, s.ID); err != nil {
		return err
	}
	for _, port := range []struct {
		port int
		what string
	}{{s.Port, "its port setting"}, {s.Dev.Port, "site.dev.port in its config"}} {
		if p.runner().PortInUse(port.port) {
			return fmt.Errorf("local: site %s: port %d is in use, by another program or a site an earlier run left behind; stop it, or give %s another port with %s", s.Deployable, port.port, s.Deployable, port.what)
		}
	}
	env := append(os.Environ(), "PORT="+strconv.Itoa(s.Dev.Port), "SUPERSCHEMATIC_SITE_URL="+s.URL)
	own := maps.Clone(direct)
	if own == nil {
		own = map[string]string{}
	}
	for service, u := range apis {
		own[service] = u.String()
	}
	for _, service := range slices.Sorted(maps.Keys(own)) {
		env = append(env, ir.EnvName(service)+"_URL="+own[service])
	}
	services := sortedServices(apis)
	c := p.out()
	stdout, stderr := c.writer(s.Deployable), c.writer(s.Deployable)
	p.printf("start site %s's dev server: bun run %s in %s, on port %d", s.Deployable, s.Dev.Command, dir, s.Dev.Port)
	proc, err := p.runner().Start(Command{Path: bun, Args: []string{"run", s.Dev.Command}, Dir: dir, Env: env, Stdout: stdout, Stderr: stderr})
	if err != nil {
		return fmt.Errorf("local: start site %s's dev server: %w", s.Deployable, err)
	}
	rs := &runningSite{site: s, proc: proc, output: []*prefixWriter{stdout, stderr}}
	stopDev := func(cause error) error {
		err := proc.Stop(p.stopTimeout())
		stdout.Flush()
		stderr.Flush()
		return errors.Join(cause, err)
	}
	if err := p.waitDevServer(ctx, s, proc); err != nil {
		return stopDev(err)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(Loopback, strconv.Itoa(s.Port)))
	if err != nil {
		return stopDev(fmt.Errorf("local: site %s: listen on %s: %w", s.Deployable, s.URL, err))
	}
	rs.server = &http.Server{Handler: OriginProxy(s.Deployable, devProxy(s.Deployable, dev), apis, direct), ReadHeaderTimeout: 10 * time.Second}
	rs.done = make(chan struct{})
	go func() {
		defer close(rs.done)
		_ = rs.server.Serve(listener)
	}()
	p.mu.Lock()
	if p.sites == nil {
		p.sites = map[string][]*runningSite{}
	}
	p.sites[k] = append(p.sites[k], rs)
	p.mu.Unlock()
	p.printf("serve site %s at %s: its dev server, and %s under %s<service>", s.Deployable, s.URL, strings.Join(services, ", "), ir.SiteDevAPIPrefix)
	return nil
}

// waitDevServer waits until a site's dev server answers HTTP on its port,
// whatever the status, and fails when it exits first or does not answer
// within the ready timeout.
func (p *Provisioner) waitDevServer(ctx context.Context, s *Site, proc Process) error {
	deadline := time.Now().Add(p.readyTimeout())
	for {
		select {
		case <-proc.Done():
			return fmt.Errorf("local: site %s's dev server exited before it answered: %s", s.Deployable, exitReason(proc.Err()))
		default:
		}
		_, err := p.runner().Get(ctx, s.Dev.URL()+"/")
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("local: site %s's dev server does not answer at %s after %s: %v; check that its command listens on PORT, or give site.dev.port the port it listens on", s.Deployable, s.Dev.URL(), p.readyTimeout(), err)
		}
		select {
		case <-proc.Done():
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(p.pollInterval()):
		}
	}
}

// stopSite stops the file server of the site this provisioner serves as
// id, if it serves one.
func (p *Provisioner) stopSite(k, id string) error {
	p.mu.Lock()
	var rs *runningSite
	list := p.sites[k]
	for i, candidate := range list {
		if candidate.site.ID == id {
			rs = candidate
			p.sites[k] = slices.Delete(slices.Clone(list), i, i+1)
			break
		}
	}
	p.mu.Unlock()
	if rs == nil {
		return nil
	}
	return p.stopFileServer(rs)
}

// stopSites stops every site of an environment.
func (p *Provisioner) stopSites(k string) error {
	p.mu.Lock()
	list := p.sites[k]
	delete(p.sites, k)
	p.mu.Unlock()
	var errs []error
	for i := len(list) - 1; i >= 0; i-- {
		errs = append(errs, p.stopFileServer(list[i]))
	}
	return errors.Join(errs...)
}

// stopFileServer stops a site's file server or proxy, and its dev server.
func (p *Provisioner) stopFileServer(rs *runningSite) error {
	p.printf("stop site %s", rs.site.Deployable)
	ctx, cancel := context.WithTimeout(context.Background(), p.stopTimeout())
	defer cancel()
	err := rs.server.Shutdown(ctx)
	<-rs.done
	if err != nil {
		err = fmt.Errorf("local: stop site %s: %w", rs.site.Deployable, err)
	}
	if rs.proc != nil {
		if stopErr := rs.proc.Stop(p.stopTimeout()); stopErr != nil {
			err = errors.Join(err, fmt.Errorf("local: stop site %s's dev server: %w", rs.site.Deployable, stopErr))
		}
		for _, w := range rs.output {
			w.Flush()
		}
	}
	return err
}

// SiteHandler serves a site's built files from root (D55): a GET or HEAD
// of ir.SiteConfigPath answers config, uncached; of a path that names a
// file under root, the file, and of a directory, its index.html; and of
// any other path the fallback file, relative to root, with 200, or 404
// when there is none. A file is served with the Cache-Control the gcp
// target gives it (ir.SiteFileCacheControl, D55, amended): a year,
// immutable, for a name that holds its content's hash, and no-cache for
// every other file and the fallback, so a browser that reloads sees a
// rebuilt site. No path reaches outside root.
func SiteHandler(root, fallback string, config []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		clean := path.Clean("/" + r.URL.Path)
		if clean == ir.SiteConfigPath {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			if r.Method == http.MethodGet {
				_, _ = w.Write(config)
			}
			return
		}
		rel := strings.TrimPrefix(clean, "/")
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err == nil && info.IsDir() {
			rel = path.Join(rel, "index.html")
		}
		if serveSiteFile(w, r, filepath.Join(root, filepath.FromSlash(rel)), ir.SiteFileCacheControl(rel, fallback)) {
			return
		}
		if fallback != "" && serveSiteFile(w, r, filepath.Join(root, filepath.FromSlash(path.Clean("/"+fallback))), ir.SiteCacheRevalidate) {
			return
		}
		http.NotFound(w, r)
	})
}

// serveSiteFile serves the regular file name with cacheControl, reporting
// false when there is none.
func serveSiteFile(w http.ResponseWriter, r *http.Request, name, cacheControl string) bool {
	f, err := os.Open(name)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	w.Header().Set("Cache-Control", cacheControl)
	http.ServeContent(w, r, info.Name(), time.Time{}, f)
	return true
}
