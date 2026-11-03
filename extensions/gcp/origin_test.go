package gcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/gcp"
)

// originShop is siteShop with shop-web serving shop-api on its origin
// (D91), and shop-api in no expose of the stack's.
func originShop(fallback string) (*ir.Stack, []stack.Service) {
	s, services := siteShop(fallback)
	s.Expose = nil
	s.Origins = []*ir.Origin{{Site: stacktest.ShopWeb, APIs: []ir.ServiceRef{stacktest.ShopAPI}}}
	return s, services
}

// TestOriginOnGCP reads shop-web serving shop-api on its origin in Staging
// (D91): the site's load balancer gains a serverless network endpoint
// group of shop-api's Cloud Run service and a backend service over it
// without Cloud CDN, in the site's rollout step after shop-api's; the URL
// map sends /api/shop-api, and every path under it, there with the path
// taken off and X-Forwarded-Prefix set, ahead of the files' rule, and the
// fallback sits on the files' rules alone. shop-api, exposed by no one but
// the origin, takes load balancers' traffic with its invoker check off and
// has no load balancer, certificate or record of its own, so the
// environment holds one certificate, the site's host's. The site's config
// names shop-api on the site's origin, and shop-api answers no CORS.
func TestOriginOnGCP(t *testing.T) {
	s, services := originShop("index.html")
	env := resolve(t, assemble(t), s, services, "Staging")
	group := node(t, env, "shop-web.api.shop-api.endpoint-group")
	wantJSON(t, "the endpoint group", group.Properties,
		`{"cloudRun":{"service":{"$output":{"resource":"shop-api.service","name":"name"}}},"name":"shop-web-shop-api-neg","networkEndpointType":"SERVERLESS","project":"acme-staging","region":"us-east1"}`)
	backend := node(t, env, "shop-web.api.shop-api.backend")
	wantJSON(t, "the API's backend", backend.Properties,
		`{"backends":[{"group":{"$output":{"resource":"shop-web.api.shop-api.endpoint-group","name":"id"}}}],"enableCdn":false,"loadBalancingScheme":"EXTERNAL_MANAGED","name":"shop-web-shop-api-api","project":"acme-staging"}`)
	matcher := node(t, env, "shop-web.url-map").Properties["pathMatchers"].([]any)[0].(map[string]any)
	if _, ok := matcher["defaultCustomErrorResponsePolicy"]; ok {
		t.Errorf("the path matcher serves the fallback for an API's 404 too: %s", mustJSON(t, matcher))
	}
	notFound := `{"errorResponseRules":[{"matchResponseCodes":["404"],"overrideResponseCode":200,"path":"/{site-digest}/index.html"}],"errorService":{"$output":{"resource":"shop-web.backend","name":"id"}}}`
	api := func(priority int, match string) string {
		return fmt.Sprintf(`{"headerAction":{"requestHeadersToAdds":[{"headerName":"X-Forwarded-Prefix","headerValue":"/api/shop-api","replace":true}]},"matchRules":[%s],"priority":%d,"routeAction":{"urlRewrite":{"pathPrefixRewrite":"/"}},"service":{"$output":{"resource":"shop-web.api.shop-api.backend","name":"id"}}}`, match, priority)
	}
	files := `{"$output":{"resource":"shop-web.backend","name":"id"}}`
	wantJSON(t, "the route rules", matcher["routeRules"], "["+strings.Join([]string{
		`{"customErrorResponsePolicy":` + notFound + `,"matchRules":[{"fullPathMatch":"/"}],"priority":1,"routeAction":{"urlRewrite":{"pathPrefixRewrite":"/{site-digest}/index.html"}},"service":` + files + `}`,
		`{"matchRules":[{"fullPathMatch":"/__superschematic/none"}],"priority":2,"service":{"$output":{"resource":"shop-web.anchor","name":"id"}}}`,
		api(3, `{"fullPathMatch":"/api/shop-api"}`),
		api(4, `{"prefixMatch":"/api/shop-api/"}`),
		`{"customErrorResponsePolicy":` + notFound + `,"matchRules":[{"prefixMatch":"/"}],"priority":5,"routeAction":{"urlRewrite":{"pathPrefixRewrite":"/{site-digest}/"}},"service":` + files + `}`,
	}, ",")+"]")

	service := node(t, env, "shop-api.service").Properties
	if service["ingress"] != "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER" || service["invokerIamDisabled"] != true {
		t.Errorf("shop-api's service takes %v with invokerIamDisabled %v; want load balancers' traffic with the check off", service["ingress"], service["invokerIamDisabled"])
	}
	if d := env.Deployable("shop-api"); d.Exposed || d.PublicAddress != nil || !slices.Equal(d.Origins, []string{"shop-web"}) {
		t.Errorf("shop-api: exposed %v at %v, origins %v", d.Exposed, d.PublicAddress, d.Origins)
	}
	for _, id := range ownedBy(env, "shop-api") {
		if strings.HasPrefix(id, "shop-api.address") || strings.HasPrefix(id, "shop-api.certificate") {
			t.Errorf("shop-api has a load balancer of its own: %s", id)
		}
	}
	var certificates []string
	for _, res := range env.Resources.Resources {
		if res.Type == gcp.TypeCertificate {
			certificates = append(certificates, mustJSON(t, res.Properties["managed"].(map[string]any)["domains"]))
		}
	}
	if !slices.Equal(certificates, []string{`["shop-web.staging.acme.dev"]`}) {
		t.Errorf("certificates = %v, want the site's host's alone", certificates)
	}
	for _, rec := range env.DNS.Records {
		if rec.Deployable != "shop-web" {
			t.Errorf("a record of %s: %s", rec.Deployable, mustJSON(t, rec.Name))
		}
	}
	wantJSON(t, "shop-web's binding", binding(t, env, "shop-web", "shop-api").Value, `{"url":"https://shop-web.staging.acme.dev/api/shop-api"}`)
	for _, b := range env.Deployable("shop-api").Bindings {
		if b.CORSOf != "" {
			t.Errorf("shop-api has the CORS field %s", b.Field)
		}
	}
	for _, step := range env.DeployOrder {
		if slices.Contains(step.Resources, "shop-web.api.shop-api.endpoint-group") && !slices.Contains(step.Deployables, "shop-web") {
			t.Errorf("the endpoint group applies in step %s %d, without shop-web", step.Step, step.Wave)
		}
	}
}

// TestOriginGolden resolves OriginShop, the shop with shop-web serving
// shop-api on its origin (D91), in Staging and in Preview, whose members
// name the endpoint group, the backend and the services under their
// parameter, and checks each environment.json, its graph and its deploy
// order, against its golden file. Resolution validated every node against
// the pinned schema of its type on the way.
func TestOriginGolden(t *testing.T) {
	reg := assemble(t)
	s, services := originShop("index.html")
	s.Name = "OriginShop"
	for _, env := range []string{"Staging", "Preview"} {
		t.Run(env, func(t *testing.T) {
			checkGolden(t, reg, s, services, env)
		})
	}
}

// TestOriginWithoutDomainOnGCP: with no domain, the site's origin is its
// load balancer's address over HTTP, where shop-api's path is, and shop-api
// still takes load balancers' traffic alone, its run.app URL not public.
func TestOriginWithoutDomainOnGCP(t *testing.T) {
	s, services := originShop("")
	for _, env := range s.Environments {
		env.Domain, env.DNS = "", nil
	}
	env := resolve(t, assemble(t), s, services, "Staging")
	wantJSON(t, "shop-web's binding", binding(t, env, "shop-web", "shop-api").Value,
		`{"url":{"$concat":["http://",{"$output":{"resource":"shop-web.address","name":"address"}},"/api/shop-api"]}}`)
	if ingress := node(t, env, "shop-api.service").Properties["ingress"]; ingress != "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER" {
		t.Errorf("shop-api's service takes %v", ingress)
	}
	matcher := node(t, env, "shop-web.url-map").Properties["pathMatchers"].([]any)[0].(map[string]any)
	if rules := matcher["routeRules"].([]any); len(rules) != 4 {
		t.Errorf("route rules without a fallback = %s, want /, the API's two and the files'", mustJSON(t, rules))
	}
}

// otherRun registers other.run, a server platform of another provider
// with a public address, and its edges to Cloud SQL and Cloud Storage.
type otherRun struct{}

func (otherRun) Name() string { return "other" }

func (otherRun) Register(r *registry.Registry) error {
	if err := r.RegisterPlatform(registry.PlatformSpec{
		Name: "other.run", Provider: "other", Kind: ir.DeployableServer, Languages: []string{registry.APILanguageGo},
		NameOf:          func(ctx registry.PlatformContext) any { return ctx.Deployable.Name },
		AddressOf:       func(ctx registry.PlatformContext) any { return "https://" + ctx.Deployable.Name + ".other.test" },
		PublicAddressOf: func(ctx registry.PlatformContext) any { return "https://" + ctx.Deployable.Name + ".other.test" },
		Lower:           func(registry.PlatformContext) (registry.Lowered, error) { return registry.Lowered{}, nil },
	}); err != nil {
		return err
	}
	return r.RegisterConnector(registry.ConnectorSpec{
		Name: "other.sql", Edge: ir.EdgeSQL, From: "other.run", To: gcp.CloudSQL,
		Connect: func(registry.ConnectorContext) (registry.Connected, error) {
			return registry.Connected{Value: ir.DatabaseConnection{URL: "postgres://shop-db"}}, nil
		},
	})
}

// TestOriginRefusesAnAPIOnAnotherProvider: a serverless network endpoint
// group reaches Cloud Run alone, so the site's load balancer refuses an
// API of its origin whose server is on another provider, which the
// generic connector would reach at its public address.
func TestOriginRefusesAnAPIOnAnotherProvider(t *testing.T) {
	reg, err := registry.Assemble(registry.DefaultNaming(), gcp.Extension{}, pulumiStub{}, otherRun{})
	if err != nil {
		t.Fatal(err)
	}
	s, services := originShop("index.html")
	s.Expose = []ir.DeployableRef{stacktest.Of(stacktest.ShopAPI)}
	media := func(e *ir.DeployableSettings) bool {
		return e.Of.Service != nil && *e.Of.Service == stacktest.ShopMedia
	}
	for _, env := range s.Environments {
		env.Settings = append(slices.DeleteFunc(env.Settings, media), &ir.DeployableSettings{Of: stacktest.Of(stacktest.ShopAPI), Platform: "other.run"})
	}
	_, err = stack.Resolve(reg, stack.Input{Stack: s, Services: stacktest.WithoutBuckets(services), Environment: "Staging"})
	if err == nil || !strings.Contains(err.Error(), "unrealizable") || !strings.Contains(err.Error(), "site shop-web serves shop-api on its origin, and shop-api's server shop-api is on another provider") {
		t.Fatalf("resolution: %v", err)
	}
}

// TestDeployOriginOnGCP deploys Staging with shop-web serving shop-api on
// its origin: the config the deploy writes names shop-api on the site's
// own origin, and the rendered URL map, pinned to the build, is what a
// browser meets. A load balancer that follows the URL map as Google's URL
// map reference documents it (matchRules by priority, pathPrefixRewrite
// on the matched part, headerAction, the custom error response of the
// rule or else the path matcher, the browser's Host passed on and
// X-Forwarded-Proto set), in front of the bucket's objects and a stand-in
// for shop-api that runs the identity runtime's cross-origin check, the
// standard library's CrossOriginProtection with no trusted origin, serves
// the config, the page and the fallback; a cookie login with
// Sec-Fetch-Site, and one with Origin alone, reach shop-api at /auth/login
// with the site's Host and X-Forwarded-Prefix; a login from another origin
// is refused; a call with the session cookie, host-only, keeps its query;
// and shop-api's 404 reaches the page as it answered it.
func TestDeployOriginOnGCP(t *testing.T) {
	f := newDeployFixture(t, true)
	s, services := originShop("index.html")
	env := resolve(t, f.reg, s, services, "Staging")
	ctx := context.Background()
	if _, err := f.cloud.EnsureSecret(ctx, "acme-staging", "Shop-PaymentsSecrets-STRIPE_KEY"); err != nil {
		t.Fatal(err)
	}
	if err := f.cloud.AddSecretVersion(ctx, "acme-staging", "Shop-PaymentsSecrets-STRIPE_KEY", []byte("sk")); err != nil {
		t.Fatal(err)
	}
	target, _ := f.reg.Target(gcp.Target)
	if err := target.Sites.Publish(ctx, registry.SitePublishRequest{Run: registry.Run{Environment: env}, Site: "shop-web", Digest: siteDigest, Dir: writeSite(t)}); err != nil {
		t.Fatal(err)
	}
	if _, err := stack.Deploy(ctx, stack.DeployOptions{
		Options: stack.Options{Registry: f.reg, Run: registry.Run{Environment: env}, Dir: t.TempDir()},
		Images:  shopImages("acme-staging", 1),
		Sites:   map[string]string{"shop-web": siteDigest},
		Planner: shopPlanner,
	}); err != nil {
		t.Fatal(err)
	}
	const host = "shop-web.staging.acme.dev"
	api := &identityStandIn{}
	objects := func(path string) ([]byte, bool) {
		data, ok := f.cloud.objects["acme-staging-shop-web"+path]
		return data, ok
	}
	lb := &urlMapLoadBalancer{
		t:       t,
		urlMap:  f.prov.Rendered().Resources.Resource("shop-web.url-map"),
		objects: objects,
		apis:    map[string]http.Handler{"shop-web.api.shop-api.backend": api},
	}
	server := httptest.NewServer(lb)
	t.Cleanup(server.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{Jar: jar, Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}}}
	call := func(method, path string, headers map[string]string) (int, http.Header, string) {
		t.Helper()
		req, err := http.NewRequest(method, "http://"+host+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		resp, err := browser.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, string(body)
	}

	status, _, body := call(http.MethodGet, ir.SiteConfigPath, nil)
	var config struct {
		APIs map[string]struct{ URL string } `json:"apis"`
	}
	if err := json.Unmarshal([]byte(body), &config); status != http.StatusOK || err != nil || config.APIs["shop-api"].URL != "https://"+host+"/api/shop-api" {
		t.Fatalf("the config: %d %s", status, body)
	}
	if status, _, body := call(http.MethodGet, "/", nil); status != http.StatusOK || !strings.Contains(body, "<!doctype html>") {
		t.Fatalf("the page: %d %q", status, body)
	}
	if status, _, body := call(http.MethodGet, "/orders/42", nil); status != http.StatusOK || !strings.Contains(body, "<!doctype html>") {
		t.Fatalf("a route of the page, the fallback: %d %q", status, body)
	}
	for _, headers := range []map[string]string{
		{"Origin": "https://" + host, "Sec-Fetch-Site": "same-origin"},
		{"Origin": "https://" + host},
	} {
		status, header, body := call(http.MethodPost, "/api/shop-api/auth/login", headers)
		if status != http.StatusNoContent {
			t.Fatalf("a cookie login with %v: %d %s", headers, status, body)
		}
		if cookie := header.Get("Set-Cookie"); cookie == "" || strings.Contains(strings.ToLower(cookie), "domain=") {
			t.Fatalf("the session cookie = %q, want one with no domain", cookie)
		}
		seen := api.last()
		if seen.URL.Path != "/auth/login" || seen.Host != host || seen.Header.Get("X-Forwarded-Prefix") != "/api/shop-api" || seen.Header.Get("X-Forwarded-Proto") != "https" {
			t.Fatalf("shop-api saw %s with Host %q, X-Forwarded-Prefix %q and X-Forwarded-Proto %q", seen.URL.Path, seen.Host, seen.Header.Get("X-Forwarded-Prefix"), seen.Header.Get("X-Forwarded-Proto"))
		}
	}
	if status, _, _ := call(http.MethodPost, "/api/shop-api/auth/login", map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}); status != http.StatusForbidden {
		t.Fatalf("a login from another origin: %d, want 403", status)
	}
	if status, _, body := call(http.MethodGet, "/api/shop-api/me?view=full", nil); status != http.StatusOK || body != `{"name":"Ada"}` || api.last().URL.RawQuery != "view=full" {
		t.Fatalf("a call with the session cookie: %d %q, query %q", status, body, api.last().URL.RawQuery)
	}
	if status, _, body := call(http.MethodGet, "/api/shop-api/orders/9", nil); status != http.StatusNotFound || !strings.Contains(body, `"not_found"`) {
		t.Fatalf("shop-api's 404: %d %q, want the API's own answer", status, body)
	}
	if status, _, _ := call(http.MethodGet, "/api/shop-api", nil); status != http.StatusNotFound || api.last().URL.Path != "/" {
		t.Fatalf("the API's own path: %d, shop-api saw %s, want /", status, api.last().URL.Path)
	}
}

// identityStandIn stands in for shop-api over the core user model (D50): a
// cookie login behind the identity runtime's cross-origin check, the
// standard library's CrossOriginProtection with the config's trusted
// origins, none here, which sets a host-only SameSite=Lax session cookie,
// as the runtime does with no cookie.domain; a route that needs the
// cookie; and a JSON 404 for any other path. It records each request.
type identityStandIn struct {
	mu   sync.Mutex
	seen []*http.Request
}

func (s *identityStandIn) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.seen = append(s.seen, r.Clone(r.Context()))
	s.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/auth/login":
		if err := http.NewCrossOriginProtection().Check(r); err != nil {
			http.Error(w, `{"code":"cross_origin"}`, http.StatusForbidden)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "tok-1", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/me":
		if c, err := r.Cookie("session"); err != nil || c.Value != "tok-1" {
			http.Error(w, `{"code":"unauthenticated"}`, http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"name":"Ada"}`)
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"code":"not_found"}`)
	}
}

func (s *identityStandIn) last() *http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[len(s.seen)-1]
}

// urlMapLoadBalancer follows a site's URL map, as the platform lowered it
// and the deploy pinned it, the way Google's URL map reference documents a
// global external Application Load Balancer's route rules: the first rule
// by priority whose matchRule matches the path, fullPathMatch exactly or
// prefixMatch as a prefix; the matched part of the path replaced by
// pathPrefixRewrite, the query kept; each of headerAction's request
// headers set; the browser's Host passed on, and X-Forwarded-Proto set as
// the load balancer's front end sets it; and a 404 from the backend
// answered with the rule's custom error response, else the path matcher's.
// A backend bucket's objects are read from objects, a backend service's
// answers from apis, by the node a rule's service names.
type urlMapLoadBalancer struct {
	t       *testing.T
	urlMap  *ir.Resource
	objects func(path string) ([]byte, bool)
	apis    map[string]http.Handler
}

func (lb *urlMapLoadBalancer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	matcher := lb.urlMap.Properties["pathMatchers"].([]any)[0].(map[string]any)
	rules := slices.Clone(matcher["routeRules"].([]any))
	slices.SortFunc(rules, func(a, b any) int {
		return int(number(a.(map[string]any)["priority"]) - number(b.(map[string]any)["priority"]))
	})
	for _, raw := range rules {
		rule := raw.(map[string]any)
		matched, ok := "", false
		for _, m := range rule["matchRules"].([]any) {
			match := m.(map[string]any)
			if full, set := match["fullPathMatch"].(string); set && r.URL.Path == full {
				matched, ok = full, true
			}
			if prefix, set := match["prefixMatch"].(string); set && strings.HasPrefix(r.URL.Path, prefix) {
				matched, ok = prefix, true
			}
		}
		if !ok {
			continue
		}
		out := r.Clone(r.Context())
		if action, set := rule["routeAction"].(map[string]any); set {
			if rewrite, set := action["urlRewrite"].(map[string]any)["pathPrefixRewrite"].(string); set {
				out.URL.Path = rewrite + strings.TrimPrefix(r.URL.Path, matched)
			}
		}
		out.Header.Set("X-Forwarded-Proto", "https")
		if action, set := rule["headerAction"].(map[string]any); set {
			for _, h := range action["requestHeadersToAdds"].([]any) {
				header := h.(map[string]any)
				out.Header.Set(header["headerName"].(string), header["headerValue"].(string))
			}
		}
		rec := httptest.NewRecorder()
		lb.backend(rule["service"]).ServeHTTP(rec, out)
		policy, set := rule["customErrorResponsePolicy"].(map[string]any)
		if !set {
			policy, _ = matcher["defaultCustomErrorResponsePolicy"].(map[string]any)
		}
		if rec.Code == http.StatusNotFound && policy != nil {
			errorRule := policy["errorResponseRules"].([]any)[0].(map[string]any)
			fallback := httptest.NewRecorder()
			page := r.Clone(r.Context())
			page.Method, page.URL.Path = http.MethodGet, errorRule["path"].(string)
			lb.backend(policy["errorService"]).ServeHTTP(fallback, page)
			fallback.Code = int(number(errorRule["overrideResponseCode"]))
			rec = fallback
		}
		for name, values := range rec.Header() {
			w.Header()[name] = values
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
		return
	}
	lb.t.Errorf("no route rule of the URL map matches %s", r.URL.Path)
	http.NotFound(w, r)
}

// backend is the handler of the node a rule's service names: the bucket's
// objects for the site's backend bucket, an API's stand-in otherwise.
func (lb *urlMapLoadBalancer) backend(service any) http.Handler {
	out, _ := service.(ir.Output)
	if out.Resource == "shop-web.backend" {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			data, ok := lb.objects(r.URL.Path)
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(data)
		})
	}
	if h, ok := lb.apis[out.Resource]; ok {
		return h
	}
	lb.t.Errorf("the URL map names service %v, which no stand-in answers", service)
	return http.NotFoundHandler()
}

// number reads a number of a property, as JSON or Go gives it.
func number(v any) float64 {
	switch v := v.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	}
	return 0
}
