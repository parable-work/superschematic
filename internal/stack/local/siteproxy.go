package local

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// The proxy in front of a site's dev server (D82). stack dev serves the
// site on its origin, the site's port on loopback, from this proxy: each
// API the site calls under SiteDevAPIPrefix and its name, its config at
// ir.SiteConfigPath, and every other path from the dev server, WebSocket
// upgrades among them, so the dev server's hot reload reaches the page. The
// page and the APIs share one origin, so a session cookie, SameSite=Lax or
// Strict, travels with each call, and the identity runtime's cross-origin
// check sees a same-origin request: no CORS and no trusted origin to set.

// SiteProxy returns the handler of a site's origin under stack dev (D82):
//
//   - GET or HEAD of ir.SiteConfigPath answers the site's config, uncached,
//     each API's URL the path the proxy serves it under, on the origin the
//     request names (http://<Host>/api/<service>), so the page reaches it
//     whether the browser opened 127.0.0.1 or localhost;
//   - /api/<service> and every path under it, for a service in apis,
//     reaches the API's server at apis[service], with the prefix taken off
//     (/api/shop-api/api/products is /api/products on shop-api's server),
//     its query kept, and its Host the browser's, so the API sees the
//     request as the page made it; a redirect the API answers within its
//     own routes gets the prefix back;
//   - any other path, a WebSocket upgrade among them, reaches the dev
//     server at dev, Host and path as they are.
//
// Answers stream as they come, so a server-sent stream reaches the page as
// it is written. An upstream that does not answer is 502, naming it.
func SiteProxy(site string, dev *url.URL, apis map[string]*url.URL) http.Handler {
	toDev := devProxy(site, dev)
	toAPI := make(map[string]http.Handler, len(apis))
	for service, target := range apis {
		toAPI[service] = apiProxy(site, service, target)
	}
	// The paths are matched as they come, not through a ServeMux, which
	// would redirect a dev server's path that holds a double slash or a
	// dot segment.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == ir.SiteConfigPath {
			serveSiteDevConfig(w, r, apis)
			return
		}
		if rest, ok := strings.CutPrefix(r.URL.Path, ir.SiteDevAPIPrefix); ok {
			service, _, _ := strings.Cut(rest, "/")
			if proxy, ok := toAPI[service]; ok {
				proxy.ServeHTTP(w, r)
				return
			}
		}
		toDev.ServeHTTP(w, r)
	})
}

// serveSiteDevConfig answers a GET or HEAD of the site's config.
func serveSiteDevConfig(w http.ResponseWriter, r *http.Request, apis map[string]*url.URL) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := siteDevConfig(r.Host, apis)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}

// siteDevConfig is the config the proxy serves a page on host: each API at
// the path the proxy serves it under, on the page's own origin.
func siteDevConfig(host string, apis map[string]*url.URL) ([]byte, error) {
	if host == "" {
		return nil, fmt.Errorf("the request names no host, which the APIs' URLs are on")
	}
	names := make([]string, 0, len(apis))
	for service := range apis {
		names = append(names, service)
	}
	sort.Strings(names)
	config := map[string]map[string]map[string]string{"apis": {}}
	for _, service := range names {
		config["apis"][service] = map[string]string{"url": "http://" + host + ir.SiteDevAPIPath(service)}
	}
	return json.Marshal(config)
}

// apiProxy forwards the paths under the API's prefix to its server at
// target, with the prefix taken off.
func apiProxy(site, service string, target *url.URL) *httputil.ReverseProxy {
	prefix := ir.SiteDevAPIPath(service)
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			rest := strings.TrimPrefix(r.In.URL.EscapedPath(), prefix)
			if rest == "" {
				rest = "/"
			}
			r.SetURL(target)
			escaped := strings.TrimSuffix(target.EscapedPath(), "/") + rest
			r.Out.URL.Path, r.Out.URL.RawPath = escaped, ""
			if unescaped, err := url.PathUnescape(escaped); err == nil && unescaped != escaped {
				r.Out.URL.Path, r.Out.URL.RawPath = unescaped, escaped
			}
			r.Out.Host = r.In.Host
			r.SetXForwarded()
			r.Out.Header.Set("X-Forwarded-Prefix", prefix)
		},
		ModifyResponse: func(resp *http.Response) error {
			if location := resp.Header.Get("Location"); location != "" {
				resp.Header.Set("Location", prefixLocation(location, prefix, target, resp.Request))
			}
			return nil
		},
		FlushInterval: -1,
		ErrorHandler:  upstreamError(fmt.Sprintf("site %s: %s at %s", site, service, target)),
	}
}

// devProxy forwards a request to the site's dev server as it is.
func devProxy(site string, dev *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(dev)
			r.Out.Host = r.In.Host
			r.SetXForwarded()
		},
		FlushInterval: -1,
		ErrorHandler:  upstreamError(fmt.Sprintf("site %s: its dev server at %s", site, dev)),
	}
}

// prefixLocation puts prefix back on a redirect an API answered within
// its own routes: a path-absolute location, or one on the API's own origin
// or on the origin the proxied request named. Any other location is left
// as it is.
func prefixLocation(location, prefix string, target *url.URL, req *http.Request) string {
	u, err := url.Parse(location)
	if err != nil {
		return location
	}
	switch {
	case u.Scheme == "" && u.Host == "" && strings.HasPrefix(u.Path, "/"):
	case u.Host == target.Host, req != nil && u.Host == req.Host:
		u.Scheme, u.Host = "", ""
	default:
		return location
	}
	u.Path = prefix + u.Path
	u.RawPath = ""
	return u.String()
}

// upstreamError answers 502 for an upstream the proxy could not reach.
func upstreamError(what string) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, fmt.Sprintf("%s does not answer: %v", what, err), http.StatusBadGateway)
	}
}
