package gcp_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/gcp"
)

// ownedBy returns the IDs of the nodes an owner produced, sorted.
func ownedBy(env *ir.ResolvedEnvironment, owner string) []string {
	var ids []string
	for _, res := range env.Resources.Resources {
		if slices.Contains(res.Owners, owner) {
			ids = append(ids, res.ID)
		}
	}
	return ids
}

func binding(t *testing.T, env *ir.ResolvedEnvironment, server, field string) *ir.Binding {
	t.Helper()
	d := env.Deployable(server)
	if d == nil {
		t.Fatalf("no deployable %s", server)
	}
	for _, b := range d.Bindings {
		if b.Field == field {
			return b
		}
	}
	t.Fatalf("%s has no binding %s", server, field)
	return nil
}

func node(t *testing.T, env *ir.ResolvedEnvironment, id string) *ir.Resource {
	t.Helper()
	res := env.Resources.Resource(id)
	if res == nil {
		t.Fatalf("no resource %s", id)
	}
	return res
}

func wantJSON(t *testing.T, what string, got any, want string) {
	t.Helper()
	if g := mustJSON(t, got); g != want {
		t.Errorf("%s =\n  %s\nwant\n  %s", what, g, want)
	}
}

// TestSQLConnectorGrants checks what the Cloud Run to Cloud SQL connector
// gives a server for its sql edge: the Cloud SQL client and instance user
// roles, each held to the edge's instance, and an IAM database user; and
// the Cloud SQL connection it derives, which the server's service carries
// as variables and mounts as its Cloud SQL connection.
func TestSQLConnectorGrants(t *testing.T) {
	env := resolve(t, assemble(t), shop(), stacktest.WithoutBuckets(stacktest.AcmeShop()), "Staging")
	edge := "sql:shop-api->shop-db"
	if got, want := strings.Join(ownedBy(env, edge), ", "), "shop-api.cloudsql-client.shop-db, shop-api.cloudsql-login.shop-db, shop-api.database-user.shop-db"; got != want {
		t.Errorf("%s produces %s, want %s", edge, got, want)
	}
	condition := map[string]any{
		"title":      "Cloud SQL instance shop-db",
		"expression": `resource.type == "sqladmin.googleapis.com/Instance" && resource.name == "projects/acme-staging/instances/shop-db"`,
	}
	for id, role := range map[string]string{
		"shop-api.cloudsql-client.shop-db": "roles/cloudsql.client",
		"shop-api.cloudsql-login.shop-db":  "roles/cloudsql.instanceUser",
	} {
		res := node(t, env, id)
		if res.Type != gcp.TypeProjectIAMMember || res.Properties["role"] != role {
			t.Errorf("%s is a %s granting %v, want a %s granting %s", id, res.Type, res.Properties["role"], gcp.TypeProjectIAMMember, role)
		}
		wantJSON(t, id+" member", res.Properties["member"], `{"$output":{"resource":"shop-api.account","name":"member"}}`)
		wantJSON(t, id+" condition", res.Properties["condition"], mustJSON(t, condition))
	}
	user := node(t, env, "shop-api.database-user.shop-db")
	wantJSON(t, "database user", user.Properties,
		`{"deletionPolicy":"ABANDON","instance":{"$output":{"resource":"shop-db.instance","name":"name"}},"name":"shop-api@acme-staging.iam","project":"acme-staging","type":"CLOUD_IAM_SERVICE_ACCOUNT"}`)
	if !slices.Contains(user.DependsOn, "shop-api.account") {
		t.Errorf("the database user depends on %v, which lacks the account it names", user.DependsOn)
	}

	b := binding(t, env, "shop-api", "SHOP_DB_DATABASE")
	wantJSON(t, "derived connection", b.Value,
		`{"cloudSql":{"database":{"$output":{"resource":"shop-db.database.shop-db","name":"name"}},"instance":{"$output":{"resource":"shop-db.instance","name":"connectionName"}},"user":"shop-api@acme-staging.iam"}}`)

	template := node(t, env, "shop-api.service").Properties["template"].(map[string]any)
	container := template["containers"].([]any)[0].(map[string]any)
	var names []string
	for _, e := range container["envs"].([]any) {
		if name := e.(map[string]any)["name"].(string); strings.HasPrefix(name, "SHOP_DB_DATABASE") {
			names = append(names, name)
		}
	}
	if got, want := strings.Join(names, ", "), "SHOP_DB_DATABASE_CLOUD_SQL_DATABASE, SHOP_DB_DATABASE_CLOUD_SQL_INSTANCE, SHOP_DB_DATABASE_CLOUD_SQL_USER"; got != want {
		t.Errorf("connection variables = %s, want %s", got, want)
	}
	wantJSON(t, "Cloud SQL volume", template["volumes"],
		`[{"cloudSqlInstance":{"instances":[{"$output":{"resource":"shop-db.instance","name":"connectionName"}}]},"name":"cloudsql"}]`)
}

// TestSQLConnectorUnderParameter checks that a preview member's database
// user is its own account's, on the instance it inherits.
func TestSQLConnectorUnderParameter(t *testing.T) {
	env := resolve(t, assemble(t), shop(), stacktest.WithoutBuckets(stacktest.AcmeShop()), "Preview")
	user := node(t, env, "shop-api.database-user.shop-db")
	if user.Inherited {
		t.Error("the preview's database user is inherited; each member creates its own")
	}
	wantJSON(t, "preview database user", user.Properties["name"], `{"$concat":["shop-api-pr",{"$parameter":"pr"},"@acme-staging.iam"]}`)
	if !node(t, env, "shop-db.instance").Inherited {
		t.Error("the preview's instance is not inherited from Staging")
	}
}

// TestSQLConnectorRefusesRust checks that a Rust server's sql edge fails
// to lower: Cloud SQL has no connector for Rust, and the password form is
// not built.
func TestSQLConnectorRefusesRust(t *testing.T) {
	services := stacktest.WithoutBuckets(stacktest.AcmeShop())
	for i := range services {
		if services[i].Name == "shop-api" {
			services[i].Language = registry.APILanguageRust
		}
	}
	_, err := stack.Resolve(assemble(t), stack.Input{Stack: shop(), Services: services, Environment: "Staging"})
	var errs *stack.Errors
	if !errors.As(err, &errs) || len(errs.List) == 0 || errs.List[0].Code != stack.CodeLowering ||
		!strings.Contains(err.Error(), "no Cloud SQL connector") {
		t.Fatalf("err = %v, want a lowering error about the Cloud SQL connector", err)
	}
}

// TestHTTPConnectorGrants checks the Cloud Run to Cloud Run connector on
// an exposed callee: the caller's account invokes the callee, the caller
// reaches the callee's run.app URL with a Google ID token for the
// callee's custom audience, which the callee's service lists, in
// Service-Authorization alone, since the callee's invoker check is off,
// and the caller's traffic leaves through the environment's VPC.
func TestHTTPConnectorGrants(t *testing.T) {
	env := resolve(t, assemble(t), shop(), stacktest.WithoutBuckets(stacktest.AcmeShop()), "Staging")
	edge := "http:Orders->shop-api"
	if got := strings.Join(ownedBy(env, edge), ", "); got != "Orders.run-invoker.shop-api" {
		t.Errorf("%s produces %s, want Orders.run-invoker.shop-api", edge, got)
	}
	grant := node(t, env, "Orders.run-invoker.shop-api")
	if grant.Type != gcp.TypeServiceIAMMember {
		t.Errorf("the invoker grant is a %s, want a %s", grant.Type, gcp.TypeServiceIAMMember)
	}
	wantJSON(t, "invoker grant", grant.Properties,
		`{"location":"us-east1","member":{"$output":{"resource":"Orders.account","name":"member"}},"name":{"$output":{"resource":"shop-api.service","name":"name"}},"project":"acme-staging","role":"roles/run.invoker"}`)

	wantJSON(t, "derived endpoint", binding(t, env, "Orders", "SHOP_API_SERVICE").Value,
		`{"credential":{"audience":"//run.googleapis.com/projects/acme-staging/locations/us-east1/services/shop-api","source":"google-id-token"},"url":{"$output":{"resource":"shop-api.service","name":"uri"}}}`)

	callee := node(t, env, "shop-api.service").Properties
	wantJSON(t, "callee's custom audiences", callee["customAudiences"], `["//run.googleapis.com/projects/acme-staging/locations/us-east1/services/shop-api"]`)
	if callee["ingress"] != "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER" || callee["invokerIamDisabled"] != true {
		t.Errorf("exposed shop-api takes %v with invokerIamDisabled %v, want the load balancer's traffic with the check off", callee["ingress"], callee["invokerIamDisabled"])
	}
	wantJSON(t, "caller's egress", node(t, env, "Orders.service").Properties["template"].(map[string]any)["vpcAccess"],
		`{"egress":"ALL_TRAFFIC","networkInterfaces":[{"network":{"$output":{"resource":"network","name":"name"}},"subnetwork":{"$output":{"resource":"network.subnet","name":"name"}}}]}`)
	if _, ok := node(t, env, "shop-api.service").Properties["template"].(map[string]any)["vpcAccess"]; ok {
		t.Error("shop-api calls no other server, yet has VPC egress")
	}
	if got := strings.Join(node(t, env, "network.nat").Owners, ", "); got != "Orders, shop-orders-ship-orders" {
		t.Errorf("the network's owners are %s, want the one calling server, Orders, and shop-orders' job, which calls what its API calls", got)
	}
}

// TestHTTPConnectorGivesTheCalleeItsCaller checks what the connector
// gives a callee with a service clause: SHOP_API_CALLERS holds Google's
// issuer and keys, shop-api's custom audience, which Orders's token is
// for, and Orders's service account by the email claim, as the deployable
// Orders that serves shop-orders; and shop-orders' job by its own
// account's email, as the deployable shop-orders-ship-orders, which
// serves shop-orders too (D52). On the server, the field is one variable
// per member, the lists of objects counted and indexed.
func TestHTTPConnectorGivesTheCalleeItsCaller(t *testing.T) {
	env := resolve(t, assemble(t), shop(), stacktest.WithoutBuckets(stacktest.RequireServiceShop()), "Staging")
	callers := binding(t, env, "shop-api", "SHOP_API_CALLERS")
	if callers.Source != ir.BindingDerived || callers.CallersOf != "shop-api" ||
		strings.Join(callers.Edges, ",") != "http:Orders->shop-api,http:shop-orders-ship-orders->shop-api" {
		t.Errorf("SHOP_API_CALLERS = %+v, want the derived callers of shop-api from Orders's edge and the job's", callers)
	}
	wantJSON(t, "SHOP_API_CALLERS", callers.Value,
		`{"issuers":[{"algorithms":["RS256"],"audience":"//run.googleapis.com/projects/acme-staging/locations/us-east1/services/shop-api","callers":[`+
			`{"deployable":"Orders","serves":["shop-orders"],"subject":"orders@acme-staging.iam.gserviceaccount.com"},`+
			`{"deployable":"shop-orders-ship-orders","serves":["shop-orders"],"subject":"shop-orders-ship-orders@acme-staging.iam.gserviceaccount.com"}],`+
			`"issuer":"https://accounts.google.com","issuerAliases":["accounts.google.com"],"jwksUrl":"https://www.googleapis.com/oauth2/v3/certs","subjectClaim":"email"}]}`)
	if err := ir.CheckServiceAuth(callers.Value); err != nil {
		t.Error(err)
	}
	for _, caller := range []string{"Orders", "shop-orders-ship-orders"} {
		token := binding(t, env, caller, "SHOP_API_SERVICE").Value.(map[string]any)["credential"].(map[string]any)["audience"]
		if token != callers.Value.(map[string]any)["issuers"].([]any)[0].(map[string]any)["audience"] {
			t.Errorf("%s's token is for %v, which shop-api does not accept", caller, token)
		}
	}

	envs, _ := node(t, env, "shop-api.service").Properties["template"].(map[string]any)["containers"].([]any)[0].(map[string]any)["envs"].([]any)
	vars := map[string]any{}
	for _, e := range envs {
		m := e.(map[string]any)
		vars[m["name"].(string)] = m["value"]
	}
	for name, want := range map[string]any{
		"SHOP_API_CALLERS_ISSUERS":                        "1",
		"SHOP_API_CALLERS_ISSUERS_0_ISSUER":               "https://accounts.google.com",
		"SHOP_API_CALLERS_ISSUERS_0_ISSUER_ALIASES":       "accounts.google.com",
		"SHOP_API_CALLERS_ISSUERS_0_ALGORITHMS":           "RS256",
		"SHOP_API_CALLERS_ISSUERS_0_JWKS_URL":             "https://www.googleapis.com/oauth2/v3/certs",
		"SHOP_API_CALLERS_ISSUERS_0_SUBJECT_CLAIM":        "email",
		"SHOP_API_CALLERS_ISSUERS_0_CALLERS":              "2",
		"SHOP_API_CALLERS_ISSUERS_0_CALLERS_0_SUBJECT":    "orders@acme-staging.iam.gserviceaccount.com",
		"SHOP_API_CALLERS_ISSUERS_0_CALLERS_0_SERVES":     "shop-orders",
		"SHOP_API_CALLERS_ISSUERS_0_CALLERS_0_DEPLOYABLE": "Orders",
		"SHOP_API_CALLERS_ISSUERS_0_CALLERS_1_SUBJECT":    "shop-orders-ship-orders@acme-staging.iam.gserviceaccount.com",
		"SHOP_API_CALLERS_ISSUERS_0_CALLERS_1_SERVES":     "shop-orders",
		"SHOP_API_CALLERS_ISSUERS_0_CALLERS_1_DEPLOYABLE": "shop-orders-ship-orders",
	} {
		if got := vars[name]; got != want {
			t.Errorf("shop-api's service sets %s to %v, want %v", name, got, want)
		}
	}
	if _, ok := vars["SHOP_API_CALLERS_ISSUERS_0_AUDIENCE"]; !ok {
		t.Error("shop-api's service does not set its audience")
	}
}

// internalShop is shop with nothing exposed and no domain.
func internalShop() *ir.Stack {
	s := shop()
	s.Expose = nil
	for _, env := range s.Environments {
		env.Domain, env.DNS = "", nil
	}
	return s
}

// TestHTTPConnectorInternalCallee checks the connector on an internal
// callee: the callee takes internal traffic only with its invoker check
// on, and the token also travels in X-Serverless-Authorization, which the
// check reads, so the end user's Authorization reaches the application.
func TestHTTPConnectorInternalCallee(t *testing.T) {
	env := resolve(t, assemble(t), internalShop(), stacktest.WithoutBuckets(stacktest.AcmeShop()), "Staging")
	wantJSON(t, "derived endpoint", binding(t, env, "Orders", "SHOP_API_SERVICE").Value,
		`{"credential":{"audience":"//run.googleapis.com/projects/acme-staging/locations/us-east1/services/shop-api","headers":["Service-Authorization","X-Serverless-Authorization"],"source":"google-id-token"},"url":{"$output":{"resource":"shop-api.service","name":"uri"}}}`)
	callee := node(t, env, "shop-api.service").Properties
	if callee["ingress"] != "INGRESS_TRAFFIC_INTERNAL_ONLY" || callee["invokerIamDisabled"] != false {
		t.Errorf("internal shop-api takes %v with invokerIamDisabled %v, want internal traffic with the check on", callee["ingress"], callee["invokerIamDisabled"])
	}
	if node(t, env, "Orders.run-invoker.shop-api") == nil {
		t.Error("no invoker grant")
	}
	for _, res := range env.Resources.Resources {
		if res.Phase == ir.PhaseExposure {
			t.Errorf("%s is an exposure resource in an environment that exposes nothing", res.ID)
		}
	}
}

// TestExposedWithoutDomain checks that an exposed server in an environment
// without a domain is reached at its run.app URL: it takes all traffic,
// and there is no load balancer and no record.
func TestExposedWithoutDomain(t *testing.T) {
	s := shop()
	for _, env := range s.Environments {
		env.Domain, env.DNS = "", nil
	}
	env := resolve(t, assemble(t), s, stacktest.WithoutBuckets(stacktest.AcmeShop()), "Staging")
	callee := node(t, env, "shop-api.service").Properties
	if callee["ingress"] != "INGRESS_TRAFFIC_ALL" || callee["invokerIamDisabled"] != true {
		t.Errorf("shop-api takes %v with invokerIamDisabled %v, want all traffic with the check off", callee["ingress"], callee["invokerIamDisabled"])
	}
	if ids := ownedBy(env, "shop-api"); slices.Contains(ids, "shop-api.forwarding-rule") || env.DNS != nil {
		t.Errorf("shop-api has %v and DNS %+v without a domain", ids, env.DNS)
	}
}

// TestHTTPConnectorSelfCall checks a server that serves both APIs: its
// call to shop-api, which it serves itself, stays on loopback with no
// grant, no credential and no VPC egress. shop-orders' job runs apart from
// the server, so its call to shop-api reaches the server's run.app URL
// through the VPC, with the invoker role and an ID token (D52).
func TestHTTPConnectorSelfCall(t *testing.T) {
	s := shop()
	s.Expose = nil
	s.Deployables = []*ir.DeployableDecl{{
		Name:   "Backend",
		Kind:   ir.DeployableServer,
		Serves: []ir.ServiceRef{stacktest.ShopAPI, stacktest.ShopOrders},
	}}
	for _, env := range s.Environments {
		env.Domain, env.DNS = "", nil
		for _, set := range env.Settings {
			if set.Of.Deployable == "Orders" {
				set.Of = ir.DeployableRef{Deployable: "Backend"}
			}
		}
	}
	env := resolve(t, assemble(t), s, stacktest.WithoutBuckets(stacktest.AcmeShop()), "Staging")
	wantJSON(t, "self endpoint", binding(t, env, "Backend", "SHOP_API_SERVICE").Value, `{"url":"http://127.0.0.1:8080"}`)
	if ids := ownedBy(env, "http:Backend->shop-api"); len(ids) > 0 {
		t.Errorf("the self edge produces %v", ids)
	}
	if got := strings.Join(node(t, env, "network").Owners, ", "); got != "shop-orders-ship-orders" {
		t.Errorf("the network's owners are %s, want the job alone: a server that calls only itself has no VPC", got)
	}
	wantJSON(t, "the job's endpoint", binding(t, env, "shop-orders-ship-orders", "SHOP_API_SERVICE").Value,
		`{"credential":{"audience":"//run.googleapis.com/projects/acme-staging/locations/us-east1/services/backend","headers":["Service-Authorization","X-Serverless-Authorization"],"source":"google-id-token"},"url":{"$output":{"resource":"Backend.service","name":"uri"}}}`)
	if got := strings.Join(ownedBy(env, "http:shop-orders-ship-orders->shop-api"), ", "); got != "shop-orders-ship-orders.run-invoker.Backend" {
		t.Errorf("the job's edge produces %s, want its invoker grant on Backend", got)
	}
}

// TestAccountIDLength checks that a server whose name makes a service
// account id GCP refuses fails to lower, before anything reaches GCP.
func TestAccountIDLength(t *testing.T) {
	s := shop()
	s.Deployables[0].Name = "OrdersFulfillmentAndInvoicingServer"
	for _, env := range s.Environments {
		for _, set := range env.Settings {
			if set.Of.Deployable == "Orders" {
				set.Of.Deployable = s.Deployables[0].Name
			}
		}
	}
	_, err := stack.Resolve(assemble(t), stack.Input{Stack: s, Services: stacktest.WithoutBuckets(stacktest.AcmeShop()), Environment: "Staging"})
	if err == nil || !strings.Contains(err.Error(), "GCP allows 30") {
		t.Fatalf("err = %v, want the service account id refused for its length", err)
	}
}
