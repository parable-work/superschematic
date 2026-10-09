package stack_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The bucket deployable and edge kinds (D54, docs/stack-model.md, section
// 8.9): shop-api lists the Bucket service shop-media in its buckets, which
// brings it into the stack as a deployable of its own, and gives shop-api's
// server a bucket edge to it whose connector derives the bucket's name.

// bucketShop returns the acceptance stack and services with shop-api's
// bucket, and without shop-orders' job, which jobShop keeps.
func bucketShop() (*ir.Stack, []stack.Service) {
	return stacktest.WithoutJobSettings(stacktest.Shop()), stacktest.WithoutJobs(stacktest.AcmeShop())
}

// TestBucketDeployable: in Staging the bucket is placed on the target's
// bucket platform, named after the project, the stack and the bucket, and
// shop-api reaches it over a bucket edge through the bucket connector,
// whose derived value fills SHOP_MEDIA_BUCKET.
func TestBucketDeployable(t *testing.T) {
	s, services := bucketShop()
	env := mustResolve(t, assemble(t), s, services, "Staging")
	bucket := env.Deployable("shop-media")
	if bucket == nil {
		t.Fatalf("no bucket shop-media among %v", names(env))
	}
	if bucket.Kind != ir.DeployableBucket || bucket.Platform != stacktest.BucketPlatform || bucket.Declared || bucket.Exposed || bucket.Language != "" {
		t.Errorf("bucket = %s %s on %s in %q, declared %t, exposed %t", bucket.Kind, bucket.Name, bucket.Platform, bucket.Language, bucket.Declared, bucket.Exposed)
	}
	if got := refNames(bucket.Services); got != "shop-media" {
		t.Errorf("services = %s, want shop-media", got)
	}
	if bucket.ResourceName != "acme-staging-shop-stack-shop-media" || len(bucket.Bindings) != 0 {
		t.Errorf("name %v with bindings %v, want acme-staging-shop-stack-shop-media with none", bucket.ResourceName, bucket.Bindings)
	}
	if got := refNames(env.Deployable("shop-api").Buckets); got != "shop-media" {
		t.Errorf("shop-api's buckets = %s, want shop-media", got)
	}
	if got := refNames(env.Deployable("Orders").Buckets); got != "" {
		t.Errorf("Orders's buckets = %s, want none: shop-orders lists none", got)
	}
	var edges []string
	for _, e := range env.Edges {
		if e.Kind == ir.EdgeBucket {
			edges = append(edges, fmt.Sprintf("%s by %s into %s", e.ID, e.Connector, e.Field))
		}
	}
	if got, want := strings.Join(edges, "; "), "bucket:shop-api->shop-media by fake.run-storage into SHOP_MEDIA_BUCKET"; got != want {
		t.Errorf("bucket edges = %s\nwant %s", got, want)
	}
	if got := bindings(env.Deployable("shop-api")); !strings.Contains(got, "SHOP_MEDIA_BUCKET=derived:bucket:shop-api->shop-media") {
		t.Errorf("shop-api's bindings = %s, want SHOP_MEDIA_BUCKET from its bucket edge", got)
	}
	for _, b := range env.Deployable("shop-api").Bindings {
		if b.Field == "SHOP_MEDIA_BUCKET" {
			if err := ir.CheckDerivedValue(ir.EdgeBucket, b.Value); err != nil {
				t.Errorf("SHOP_MEDIA_BUCKET = %v: %v", b.Value, err)
			}
		}
	}
	// A bucket is infrastructure: it and its grant apply before any server
	// rolls out.
	for _, step := range env.DeployOrder {
		for _, id := range step.Resources {
			if (id == "shop-media.bucket" || id == "shop-api.storage.shop-media") && step.Step != ir.StepInfrastructure {
				t.Errorf("%s lands in %s, want infrastructure", id, step.Step)
			}
		}
	}
}

// TestBucketSettings: Production's settings keep shop-media's object
// versions, and a member of Preview, a parameterized environment, names a
// bucket of its own under the parameter.
func TestBucketSettings(t *testing.T) {
	reg := assemble(t)
	s, services := bucketShop()
	production := mustResolve(t, reg, s, services, "Production").Deployable("shop-media")
	if production.Settings["versioning"] != true {
		t.Errorf("Production's settings = %v, want versioning", production.Settings)
	}
	s, services = bucketShop()
	preview := mustResolve(t, reg, s, services, "Preview").Deployable("shop-media")
	if got := fmt.Sprint(preview.ResourceName); !strings.Contains(got, "pr") || preview.ResourceName == "acme-staging-shop-stack-shop-media" {
		t.Errorf("Preview's bucket is named %v, want one under the parameter pr", preview.ResourceName)
	}
}

// TestBucketSharedByTwoAPIsOfOneServer: two APIs that list one bucket, on
// one declared server, give the server one bucket edge and one field, and
// a job of one of them its own edge to the same bucket.
func TestBucketSharedByTwoAPIsOfOneServer(t *testing.T) {
	s, _ := bucketShop()
	_, services := jobShop()
	service(services, "shop-orders").Buckets = []ir.ServiceRef{stacktest.ShopMedia}
	s.Deployables = []*ir.DeployableDecl{{Name: "Backend", Kind: ir.DeployableServer, Serves: []ir.ServiceRef{stacktest.ShopAPI, stacktest.ShopOrders}}}
	s.Expose = nil
	for _, env := range s.Environments {
		env.Settings = []*ir.DeployableSettings{{Of: ir.DeployableRef{Deployable: "Backend"}, Env: map[string]ir.EnvValue{"FULFILLMENT_REGION": {Value: "us"}}}}
	}
	env := mustResolve(t, assemble(t), s, services, "Staging")
	if got := refNames(env.Deployable("Backend").Buckets); got != "shop-media" {
		t.Errorf("Backend's buckets = %s, want shop-media once", got)
	}
	var edges, fields []string
	for _, e := range env.Edges {
		if e.Kind == ir.EdgeBucket {
			edges = append(edges, e.ID+" by "+e.Connector)
		}
	}
	for _, b := range env.Deployable("Backend").Bindings {
		if strings.HasPrefix(b.Field, "SHOP_MEDIA") {
			fields = append(fields, b.Field)
		}
	}
	if got, want := strings.Join(edges, "; "), "bucket:Backend->shop-media by fake.run-storage; bucket:shop-orders-ship-orders->shop-media by fake.job-storage"; got != want {
		t.Errorf("bucket edges = %s\nwant %s", got, want)
	}
	if got := strings.Join(fields, ","); got != "SHOP_MEDIA_BUCKET" {
		t.Errorf("Backend's bucket fields = %s, want SHOP_MEDIA_BUCKET once", got)
	}
}

// TestBucketFieldFromTheNamingFile: the naming file's [derived_fields]
// bucket template names the field, as envgen names it.
func TestBucketFieldFromTheNamingFile(t *testing.T) {
	s, services := bucketShop()
	env, err := stack.Resolve(assemble(t), stack.Input{Stack: s, Services: services, Environment: "Staging", FieldNames: ir.DerivedFieldNames{Bucket: "STORE_{SERVICE}"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range env.Edges {
		if e.Kind == ir.EdgeBucket && e.Field != "STORE_SHOP_MEDIA" {
			t.Errorf("edge %s fills %s, want STORE_SHOP_MEDIA", e.ID, e.Field)
		}
	}
}

// TestBucketRefusals: what resolution refuses of buckets.
func TestBucketRefusals(t *testing.T) {
	reg := assemble(t)
	media := stacktest.Of(stacktest.ShopMedia)
	t.Run("an API handle in buckets", func(t *testing.T) {
		s, services := bucketShop()
		service(services, "shop-api").Buckets = []ir.ServiceRef{stacktest.ShopOrders}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeKindMismatch, "service shop-api buckets names shop-orders, a API service; it takes Bucket services")
	})
	t.Run("a handle of the wrong kind", func(t *testing.T) {
		s, services := bucketShop()
		service(services, "shop-api").Buckets = []ir.ServiceRef{{Name: "shop-media", Kind: ir.SchemaKindDB}}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeKindMismatch, "holds a handle to shop-media of kind DB, but shop-media is a Bucket service")
	})
	t.Run("an exposed bucket", func(t *testing.T) {
		s, services := bucketShop()
		s.Expose = append(s.Expose, media)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeExposeNotServer, "exposes shop-media, a bucket; only a server is exposed")
	})
	t.Run("env on a bucket", func(t *testing.T) {
		s, services := bucketShop()
		settingsFor(s.Environment("Staging"), media).Env = map[string]ir.EnvValue{"LOG_LEVEL": {Value: "debug"}}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnknownEnvKey, "sets env LOG_LEVEL on bucket shop-media, which has no config")
	})
	t.Run("a setting the bucket platform lacks", func(t *testing.T) {
		s, services := bucketShop()
		settingsFor(s.Environment("Staging"), media).Values = map[string]any{"public": true}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidSettings, "settings of shop-media on platform fake.storage")
	})
	t.Run("a setting that claims the bucket field", func(t *testing.T) {
		s, services := bucketShop()
		api := service(services, "shop-api")
		api.Config.Fields = append(api.Config.Fields, stack.ConfigField{Name: "SHOP_MEDIA_BUCKET_REGION"})
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeFieldCollision, "config field SHOP_MEDIA_BUCKET_REGION of ShopApiConfig collides with SHOP_MEDIA_BUCKET")
	})
	t.Run("a target with no bucket platform", func(t *testing.T) {
		s, services := bucketShop()
		for _, env := range s.Environments {
			if env.Target != "" {
				env.Target = "no-dns"
				env.DNS = nil
			}
		}
		_, errs := resolve(t, assemble(t, &noDNS{}), s, services, "Staging")
		mustFail(t, errs, stack.CodeUnrealizable, "bucket shop-media: target no-dns has no platform for a bucket; place it with a settings platform")
	})
}
