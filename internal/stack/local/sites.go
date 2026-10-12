package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The local target's sites (D55; docs/stack-model.md, section 8.3). A
// site's node names its package under the output root, the script that
// builds it and the directory the build writes. At its rollout wave, once
// the servers it calls are ready, the provisioner installs the Bun
// workspace, runs the build once, and serves the directory from a file
// server of its own on loopback: each file as it is, the site's config at
// ir.SiteConfigPath, and the single-page fallback for a path that names no
// file. The config and every HTML file are served uncached, so a reload
// sees a rebuilt site.

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
}

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
	s.URL = ServerURL(s.Port)
	return s, nil
}

func (p *Program) site(id string) *Site {
	for _, s := range p.Sites {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// runningSite is a site's file server the provisioner started.
type runningSite struct {
	site   *Site
	server *http.Server
	done   chan struct{}
}

// startSites builds each site once, after one install of the Bun
// workspace, and serves its output on its port. A site this provisioner
// serves already is stopped first.
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
		dir := filepath.Join(req.RepositoryRoot, filepath.FromSlash(s.Dir))
		if info, err := os.Stat(filepath.Join(dir, "package.json")); err != nil || info.IsDir() {
			return fmt.Errorf("local: site %s: no package at %s; the site's build scaffolds it (docs/stack-model.md, section 8.10)", s.Deployable, dir)
		}
		if err := p.installWorkspace(ctx, req.OutputRoot, bun); err != nil {
			return err
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
		listener, err := net.Listen("tcp", net.JoinHostPort(Loopback, strconv.Itoa(s.Port)))
		if err != nil {
			return fmt.Errorf("local: site %s: listen on %s: %w", s.Deployable, s.URL, err)
		}
		rs := &runningSite{
			site:   s,
			server: &http.Server{Handler: SiteHandler(root, s.Fallback, config), ReadHeaderTimeout: 10 * time.Second},
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
		p.printf("serve site %s at %s from %s", s.Deployable, s.URL, root)
	}
	return nil
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

func (p *Provisioner) stopFileServer(rs *runningSite) error {
	p.printf("stop site %s", rs.site.Deployable)
	ctx, cancel := context.WithTimeout(context.Background(), p.stopTimeout())
	defer cancel()
	err := rs.server.Shutdown(ctx)
	<-rs.done
	if err != nil {
		return fmt.Errorf("local: stop site %s: %w", rs.site.Deployable, err)
	}
	return nil
}

// SiteHandler serves a site's built files from root (D55): a GET or HEAD
// of ir.SiteConfigPath answers config, uncached; of a path that names a
// file under root, the file, and of a directory, its index.html; and of
// any other path the fallback file, relative to root, with 200, or 404
// when there is none. An HTML file is served uncached too, so a browser
// that reloads sees a rebuilt site; the other files carry no cache header.
// No path reaches outside root.
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
		name := filepath.Join(root, filepath.FromSlash(clean))
		if info, err := os.Stat(name); err == nil && info.IsDir() {
			name = filepath.Join(name, "index.html")
		}
		if serveSiteFile(w, r, name) {
			return
		}
		if fallback != "" && serveSiteFile(w, r, filepath.Join(root, filepath.FromSlash(path.Clean("/"+fallback)))) {
			return
		}
		http.NotFound(w, r)
	})
}

// serveSiteFile serves the regular file name, reporting false when there
// is none.
func serveSiteFile(w http.ResponseWriter, r *http.Request, name string) bool {
	f, err := os.Open(name)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if strings.EqualFold(filepath.Ext(name), ".html") {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, info.Name(), time.Time{}, f)
	return true
}
