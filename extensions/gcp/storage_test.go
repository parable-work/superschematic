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

// TestBucketPlatform checks the Cloud Storage platform (D54): shop-media is
// a private bucket in each environment's region, named after the project,
// the stack and the Bucket service, which Staging's settings give a
// lifecycle rule and Production's versioning; a Preview member's bucket
// is its own, named with the parameter's value, and its destroy empties
// it. The deployable's address is the bucket's name, an output of it.
func TestBucketPlatform(t *testing.T) {
	reg := assemble(t)
	for _, c := range []struct {
		env, props string
	}{
		{"Staging", `{"forceDestroy":false,"lifecycleRules":[{"action":{"type":"Delete"},"condition":{"age":30}}],"location":"US-EAST1",` +
			`"name":"acme-staging-shop-shop-media","project":"acme-staging","publicAccessPrevention":"enforced","uniformBucketLevelAccess":true,"versioning":{"enabled":false}}`},
		{"Production", `{"forceDestroy":false,"location":"US-EAST1",` +
			`"name":"acme-prod-shop-shop-media","project":"acme-prod","publicAccessPrevention":"enforced","uniformBucketLevelAccess":true,"versioning":{"enabled":true}}`},
		{"Preview", `{"forceDestroy":true,"lifecycleRules":[{"action":{"type":"Delete"},"condition":{"age":30}}],"location":"US-EAST1",` +
			`"name":{"$concat":["acme-staging-shop-shop-media-pr",{"$parameter":"pr"}]},"project":"acme-staging","publicAccessPrevention":"enforced","uniformBucketLevelAccess":true,"versioning":{"enabled":false}}`},
	} {
		t.Run(c.env, func(t *testing.T) {
			env := resolve(t, reg, shop(), stacktest.AcmeShop(), c.env)
			d := env.Deployable("shop-media")
			if d == nil || d.Kind != ir.DeployableBucket || d.Platform != gcp.Storage {
				t.Fatalf("shop-media is %+v, want a bucket on %s", d, gcp.Storage)
			}
			wantJSON(t, "the address", d.Address, `{"$output":{"resource":"shop-media.bucket","name":"name"}}`)
			if got := ownedBy(env, "shop-media"); !slices.Equal(got, []string{"shop-media.bucket"}) {
				t.Errorf("shop-media produces %v, want its bucket alone", got)
			}
			bucket := node(t, env, "shop-media.bucket")
			if bucket.Type != gcp.TypeBucket || bucket.Phase != ir.PhaseInfrastructure || bucket.Inherited {
				t.Errorf("the bucket is a %s in %s, inherited %v; want a %s of its own in %s", bucket.Type, bucket.Phase, bucket.Inherited, gcp.TypeBucket, ir.PhaseInfrastructure)
			}
			wantJSON(t, "the bucket", bucket.Properties, c.props)
		})
	}
}

// TestBucketRefusals checks what the platform refuses: settings outside
// its schema, a name longer than Cloud Storage allows, and a name that
// holds `google`, which Cloud Storage refuses.
func TestBucketRefusals(t *testing.T) {
	reg := assemble(t)
	refused := func(t *testing.T, s *ir.Stack, code stack.Code, want string) {
		t.Helper()
		_, err := stack.Resolve(reg, stack.Input{Stack: s, Services: stacktest.AcmeShop(), Environment: "Staging"})
		var errs *stack.Errors
		if !errors.As(err, &errs) || !slices.ContainsFunc(errs.List, func(e stack.Error) bool {
			return e.Code == code && strings.Contains(e.Error(), want)
		}) {
			t.Errorf("err = %v, want a %s error saying %q", err, code, want)
		}
	}
	settings := func(values map[string]any) *ir.Stack {
		s := shop()
		for _, set := range s.Environment("Staging").Settings {
			if set.Of.Service != nil && *set.Of.Service == stacktest.ShopMedia {
				set.Values = values
			}
		}
		return s
	}
	t.Run("deleteAfterDays below 1", func(t *testing.T) {
		refused(t, settings(map[string]any{"deleteAfterDays": float64(0)}), stack.CodeInvalidSettings, "shop-media")
	})
	t.Run("a public bucket", func(t *testing.T) {
		refused(t, settings(map[string]any{"public": true}), stack.CodeInvalidSettings, "shop-media")
	})
	t.Run("a long name", func(t *testing.T) {
		s := shop()
		s.Name = "TheShopStackOfTheAcmeCompanyStorefront"
		env, err := stack.Resolve(reg, stack.Input{Stack: s, Services: stacktest.AcmeShop(), Environment: "Staging"})
		if err != nil {
			t.Fatal(err)
		}
		bucket := node(t, env, "shop-media.bucket").Properties
		if name, _ := bucket["name"].(string); len(name) > 63 || !strings.HasPrefix(name, "acme-staging-the-shop-stack-") {
			t.Errorf("the bucket is named %v, want a prefix and a hash in 63 characters (D97)", bucket["name"])
		}
		wantJSON(t, "the bucket's labels", bucket["labels"], `{"superschematic-deployable":"shop-media"}`)
	})
	t.Run("google", func(t *testing.T) {
		s := shop()
		s.Name = "GoogleShop"
		refused(t, s, stack.CodeLowering, `holds "google"`)
	})
}

// withBuckets returns the acme-shop services with each named API listing
// the buckets given, beside its own, and a Bucket service for each bucket
// none had.
func withBuckets(apis []string, buckets ...ir.ServiceRef) []stack.Service {
	services := stacktest.AcmeShop()
	for i := range services {
		if slices.Contains(apis, services[i].Name) {
			for _, b := range buckets {
				if !slices.Contains(services[i].Buckets, b) {
					services[i].Buckets = append(services[i].Buckets, b)
				}
			}
		}
	}
	for _, b := range buckets {
		if !slices.ContainsFunc(services, func(s stack.Service) bool { return s.Name == b.Name }) {
			services = append(services, stack.Service{Name: b.Name, Kind: ir.SchemaKindBucket})
		}
	}
	return services
}

// TestBucketConnectorGrants checks what the Cloud Run to Cloud Storage
// connector gives a server for its bucket edge: the object user role on
// the bucket alone, the right to sign as its own account, which signing
// a URL through IAM's signBlob needs, and the bucket's name, with no
// emulator's endpoint, which the server's service carries as its one
// variable.
func TestBucketConnectorGrants(t *testing.T) {
	env := resolve(t, assemble(t), shop(), stacktest.AcmeShop(), "Staging")
	edge := "bucket:shop-api->shop-media"
	if got, want := strings.Join(ownedBy(env, edge), ", "), "shop-api.sign-as-self, shop-api.storage.shop-media"; got != want {
		t.Errorf("%s produces %s, want %s", edge, got, want)
	}
	for _, e := range env.Edges {
		if e.ID == edge && e.Connector != gcp.BucketConnector {
			t.Errorf("%s is connected by %s, want %s", edge, e.Connector, gcp.BucketConnector)
		}
	}
	grant := node(t, env, "shop-api.storage.shop-media")
	if grant.Type != gcp.TypeBucketIAMMember {
		t.Errorf("the bucket grant is a %s, want a %s", grant.Type, gcp.TypeBucketIAMMember)
	}
	wantJSON(t, "the bucket grant", grant.Properties,
		`{"bucket":{"$output":{"resource":"shop-media.bucket","name":"name"}},"member":{"$output":{"resource":"shop-api.account","name":"member"}},"role":"roles/storage.objectUser"}`)
	sign := node(t, env, "shop-api.sign-as-self")
	if sign.Type != gcp.TypeServiceAccountIAMMember {
		t.Errorf("the signing grant is a %s, want a %s", sign.Type, gcp.TypeServiceAccountIAMMember)
	}
	wantJSON(t, "the signing grant", sign.Properties,
		`{"member":{"$output":{"resource":"shop-api.account","name":"member"}},"role":"roles/iam.serviceAccountTokenCreator","serviceAccountId":{"$output":{"resource":"shop-api.account","name":"name"}}}`)

	wantJSON(t, "the derived bucket connection", binding(t, env, "shop-api", "SHOP_MEDIA_BUCKET").Value,
		`{"name":{"$output":{"resource":"shop-media.bucket","name":"name"}}}`)
	template := node(t, env, "shop-api.service").Properties["template"].(map[string]any)
	var names []string
	for _, e := range template["containers"].([]any)[0].(map[string]any)["envs"].([]any) {
		if name := e.(map[string]any)["name"].(string); strings.HasPrefix(name, "SHOP_MEDIA_BUCKET") {
			names = append(names, name)
		}
	}
	if got := strings.Join(names, ", "); got != "SHOP_MEDIA_BUCKET_NAME" {
		t.Errorf("bucket variables = %s, want SHOP_MEDIA_BUCKET_NAME alone", got)
	}
}

// TestBucketConnectorSharesTheSigningGrant checks that a server with two
// bucket edges holds one signing grant, which both edges produce, and a
// grant on each bucket.
func TestBucketConnectorSharesTheSigningGrant(t *testing.T) {
	archive := ir.ServiceRef{Name: "shop-archive", Kind: ir.SchemaKindBucket}
	env := resolve(t, assemble(t), shop(), withBuckets([]string{"shop-api"}, archive), "Staging")
	if got, want := strings.Join(ownedBy(env, "bucket:shop-api->shop-archive"), ", "), "shop-api.sign-as-self, shop-api.storage.shop-archive"; got != want {
		t.Errorf("the archive's edge produces %s, want %s", got, want)
	}
	if got := node(t, env, "shop-api.sign-as-self").Owners; !slices.Equal(got, []string{"bucket:shop-api->shop-archive", "bucket:shop-api->shop-media"}) {
		t.Errorf("the signing grant's owners = %v, want both edges", got)
	}
	wantJSON(t, "the archive's name", node(t, env, "shop-archive.bucket").Properties["name"], `"acme-staging-shop-shop-archive"`)
}

// TestBucketConnectorFromAJob checks the Cloud Run job to Cloud Storage
// connector: a job takes its API's bucket edges (D52), with the grants a
// server's take, for its own account.
func TestBucketConnectorFromAJob(t *testing.T) {
	env := resolve(t, assemble(t), shop(), withBuckets([]string{"shop-orders"}, stacktest.ShopMedia), "Staging")
	job := stacktest.ShipOrdersJob
	edge := "bucket:" + job + "->shop-media"
	found := false
	for _, e := range env.Edges {
		if e.ID == edge {
			found = true
			if e.Connector != gcp.JobBucketConnector {
				t.Errorf("%s is connected by %s, want %s", edge, e.Connector, gcp.JobBucketConnector)
			}
		}
	}
	if !found {
		t.Fatalf("no edge %s", edge)
	}
	if got, want := strings.Join(ownedBy(env, edge), ", "), job+".sign-as-self, "+job+".storage.shop-media"; got != want {
		t.Errorf("%s produces %s, want %s", edge, got, want)
	}
	wantJSON(t, "the job's grant member", node(t, env, job+".storage.shop-media").Properties["member"],
		`{"$output":{"resource":"`+job+`.account","name":"member"}}`)
	wantJSON(t, "the job's bucket connection", binding(t, env, job, "SHOP_MEDIA_BUCKET").Value,
		`{"name":{"$output":{"resource":"shop-media.bucket","name":"name"}}}`)
}

// TestBucketConnectorServesRust checks that a Rust server's bucket edge
// lowers as a Go server's does (D54): the object user role on the bucket
// and the right to sign as its own account, which the Rust runtime's gcs
// signs a URL through IAM's signBlob with, and the bucket's name alone,
// which it reaches as the account the metadata server's token is for.
func TestBucketConnectorServesRust(t *testing.T) {
	reg := assemble(t)
	goEnv := resolve(t, reg, shop(), stacktest.AcmeShop(), "Staging")
	env := resolve(t, reg, shop(), stacktest.InRust(stacktest.AcmeShop(), "shop-api"), "Staging")
	if got := env.Deployable("shop-api").Language; got != registry.APILanguageRust {
		t.Fatalf("shop-api's server is %s, want %s", got, registry.APILanguageRust)
	}
	edge := "bucket:shop-api->shop-media"
	ids, goIDs := ownedBy(env, edge), ownedBy(goEnv, edge)
	if got, want := strings.Join(ids, ", "), "shop-api.sign-as-self, shop-api.storage.shop-media"; got != want || !slices.Equal(ids, goIDs) {
		t.Fatalf("%s produces %s, want %s, as a Go server's produces %v", edge, got, want, goIDs)
	}
	for _, id := range ids {
		res, goRes := node(t, env, id), node(t, goEnv, id)
		if res.Type != goRes.Type {
			t.Errorf("%s is a %s, and a %s for a Go server", id, res.Type, goRes.Type)
		}
		wantJSON(t, id, res.Properties, mustJSON(t, goRes.Properties))
	}
	wantJSON(t, "the derived bucket connection", binding(t, env, "shop-api", "SHOP_MEDIA_BUCKET").Value,
		`{"name":{"$output":{"resource":"shop-media.bucket","name":"name"}}}`)
}

// TestPolicyPrivateBuckets runs buckets-never-public over a resolved
// Staging changed one way at a time: each change makes shop-media's
// bucket reachable by more than IAM's grants, or grants it to the public,
// on behalf of the exposed shop-api too, which nothing-public-unless-
// exposed admits. A bucket of another deployable's, as a static site's
// would be (D55), is not the rule's.
func TestPolicyPrivateBuckets(t *testing.T) {
	reg := assemble(t)
	rule := policy(t, reg, gcp.PolicyPrivateBuckets)
	resolved := func() *ir.ResolvedEnvironment {
		return resolve(t, reg, shop(), stacktest.AcmeShop(), "Staging")
	}
	if findings := rule.Check(resolved()); len(findings) > 0 {
		t.Fatalf("Staging as resolved has findings: %v", findings)
	}
	publicGrant := func(owner string) *ir.Resource {
		return &ir.Resource{
			ID:   owner + ".public-media",
			Type: gcp.TypeBucketIAMMember,
			Properties: map[string]any{
				"bucket": ir.Output{Resource: "shop-media.bucket", Name: "name"},
				"role":   "roles/storage.objectViewer",
				"member": "allUsers",
			},
			Owners: []string{owner},
		}
	}
	cases := []struct {
		name   string
		change func(env *ir.ResolvedEnvironment)
		want   string
	}{
		{"object ACLs", func(env *ir.ResolvedEnvironment) {
			env.Resources.Resource("shop-media.bucket").Properties["uniformBucketLevelAccess"] = false
		}, "bucket shop-media.bucket of shop-media does not set uniform bucket-level access"},
		{"public access prevention", func(env *ir.ResolvedEnvironment) {
			env.Resources.Resource("shop-media.bucket").Properties["publicAccessPrevention"] = "inherited"
		}, "does not enforce public access prevention"},
		{"a public grant from an exposed server", func(env *ir.ResolvedEnvironment) {
			env.Resources.Resources = append(env.Resources.Resources, publicGrant("shop-api"))
		}, "shop-api.public-media grants roles/storage.objectViewer to allUsers on bucket shop-media.bucket"},
		{"the edge's grant made public", func(env *ir.ResolvedEnvironment) {
			env.Resources.Resource("shop-api.storage.shop-media").Properties["member"] = "allAuthenticatedUsers"
		}, "to allAuthenticatedUsers on bucket shop-media.bucket"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := resolved()
			c.change(env)
			findings := rule.Check(env)
			if len(findings) != 1 || !strings.Contains(findings[0], c.want) {
				t.Errorf("findings = %q, want one containing %q", findings, c.want)
			}
		})
	}

	// nothing-public-unless-exposed still refuses the public grant on
	// behalf of a deployable that is not exposed.
	env := resolved()
	env.Resources.Resources = append(env.Resources.Resources, publicGrant("Orders"))
	if findings := policy(t, reg, gcp.PolicyNothingPublic).Check(env); len(findings) != 1 || !strings.Contains(findings[0], "Orders.public-media grants") {
		t.Errorf("nothing-public-unless-exposed findings = %q, want one on Orders.public-media", findings)
	}

	env = resolved()
	env.Resources.Resources = append(env.Resources.Resources, &ir.Resource{
		ID:         "shop-api.site",
		Type:       gcp.TypeBucket,
		Properties: map[string]any{"publicAccessPrevention": "inherited"},
		Owners:     []string{"shop-api"},
	})
	if findings := rule.Check(env); len(findings) > 0 {
		t.Errorf("a bucket of a server has findings: %v", findings)
	}
}
