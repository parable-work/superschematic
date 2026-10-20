package cloudflare_test

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// TestR2Bucket reads the calls fixture's bucket and the storefront's edge
// to it (sections 18.2 and 18.7):
//
//   - Staging's bucket is `calls-stack-staging-shop-media`, in western Europe, and
//     its lifecycle deletes each object 30 days after it was written;
//     Production's stores new objects as infrequently accessed and has no
//     lifecycle;
//   - the edge mints an account token holding Workers R2 Storage Bucket
//     Item Write on that bucket's objects alone;
//   - the storefront's value is the bucket's name, its r2_bucket binding
//     and the token's id and value, which its version holds as plain and
//     secret text beside the r2_bucket binding.
func TestR2Bucket(t *testing.T) {
	reg := assemble(t)
	env := resolveCalls(t, reg, "Staging")
	media := env.Deployable("shop-media")
	if media.Platform != cloudflare.R2 || media.ResourceName != "calls-stack-staging-shop-media" {
		t.Errorf("the bucket is placed as %s, named %v", media.Platform, media.ResourceName)
	}
	bucket := node(t, env, "shop-media.bucket")
	if bucket.Type != cloudflare.TypeR2Bucket || bucket.Phase != ir.PhaseInfrastructure {
		t.Errorf("the bucket is %s in %s", bucket.Type, bucket.Phase)
	}
	if got, want := jsonOf(t, bucket.Properties), `{"accountId":"`+accountID+`","location":"weur","name":"calls-stack-staging-shop-media"}`; got != want {
		t.Errorf("the bucket is %s, want %s", got, want)
	}
	lifecycle := node(t, env, "shop-media.lifecycle")
	if got, want := jsonOf(t, lifecycle.Properties["rules"]), `[{"conditions":{"prefix":""},"deleteObjectsTransition":{"condition":{"maxAge":2592000,"type":"Age"}},"enabled":true,"id":"delete-after-30-days"}]`; got != want {
		t.Errorf("the lifecycle's rules are %s, want %s", got, want)
	}
	if got := jsonOf(t, lifecycle.Properties["bucketName"]); got != `{"$output":{"resource":"shop-media.bucket","name":"name"}}` {
		t.Errorf("the lifecycle names bucket %s", got)
	}

	token := node(t, env, "shop-storefront.r2.shop-media.token")
	if token.Type != cloudflare.TypeAccountToken || token.Phase != ir.PhaseInfrastructure || token.Properties["name"] != "calls-stack-staging-shop-storefront-shop-media" {
		t.Errorf("the edge's token is %s in %s named %v", token.Type, token.Phase, token.Properties["name"])
	}
	want := `[{"effect":"allow","permissionGroups":[{"id":"` + cloudflare.R2ObjectsWriteGroup + `"}],` +
		`"resources":{"$concat":["{\"com.cloudflare.edge.r2.bucket.` + accountID + `_default_",{"$output":{"resource":"shop-media.bucket","name":"name"}},"\":\"*\"}"]}}]`
	if got := jsonOf(t, token.Properties["policies"]); got != want {
		t.Errorf("the token's policies are\n%s, want\n%s", got, want)
	}

	want = `{"binding":"SHOP_MEDIA_BUCKET","name":{"$output":{"resource":"shop-media.bucket","name":"name"}},` +
		`"r2":{"accessKeyId":{"$output":{"resource":"shop-storefront.r2.shop-media.token","name":"id"}},"account":"` + accountID + `",` +
		`"token":{"$output":{"resource":"shop-storefront.r2.shop-media.token","name":"value"}}}}`
	if got := derivedValue(t, env, "shop-storefront", "SHOP_MEDIA_BUCKET"); got != want {
		t.Errorf("the storefront's bucket is\n%s, want\n%s", got, want)
	}
	bindings := bindingsOf(t, node(t, env, "shop-storefront.version"))
	for name, want := range map[string]string{
		"SHOP_MEDIA_BUCKET":                  `{"bucketName":{"$output":{"resource":"shop-media.bucket","name":"name"}},"name":"SHOP_MEDIA_BUCKET","type":"r2_bucket"}`,
		"SHOP_MEDIA_BUCKET_BINDING":          `{"name":"SHOP_MEDIA_BUCKET_BINDING","text":"SHOP_MEDIA_BUCKET","type":"plain_text"}`,
		"SHOP_MEDIA_BUCKET_NAME":             `{"name":"SHOP_MEDIA_BUCKET_NAME","text":{"$output":{"resource":"shop-media.bucket","name":"name"}},"type":"plain_text"}`,
		"SHOP_MEDIA_BUCKET_R2_ACCOUNT":       `{"name":"SHOP_MEDIA_BUCKET_R2_ACCOUNT","text":"` + accountID + `","type":"plain_text"}`,
		"SHOP_MEDIA_BUCKET_R2_ACCESS_KEY_ID": `{"name":"SHOP_MEDIA_BUCKET_R2_ACCESS_KEY_ID","text":{"$output":{"resource":"shop-storefront.r2.shop-media.token","name":"id"}},"type":"plain_text"}`,
		"SHOP_MEDIA_BUCKET_R2_TOKEN":         `{"name":"SHOP_MEDIA_BUCKET_R2_TOKEN","text":{"$output":{"resource":"shop-storefront.r2.shop-media.token","name":"value"}},"type":"secret_text"}`,
	} {
		if bindings[name] != want {
			t.Errorf("the storefront's binding %s is\n%s, want\n%s", name, bindings[name], want)
		}
	}

	production := resolveCalls(t, reg, "Production")
	if got, want := jsonOf(t, node(t, production, "shop-media.bucket").Properties), `{"accountId":"`+accountID+`","name":"calls-stack-production-shop-media","storageClass":"InfrequentAccess"}`; got != want {
		t.Errorf("Production's bucket is %s, want %s", got, want)
	}
	if production.Resources.Resource("shop-media.lifecycle") != nil {
		t.Error("Production's bucket, with no deleteAfterDays, has a lifecycle")
	}

	preview := resolveCalls(t, reg, "Preview")
	if got := jsonOf(t, node(t, preview, "shop-media.bucket").Properties["name"]); got != `{"$concat":["calls-stack-preview-shop-media-pr",{"$parameter":"pr"}]}` {
		t.Errorf("the member's bucket is named %s", got)
	}
	if got := jsonOf(t, node(t, preview, "shop-storefront.r2.shop-media.token").Properties["name"]); got != `{"$concat":["calls-stack-preview-shop-storefront-shop-media-pr",{"$parameter":"pr"}]}` {
		t.Errorf("the member's token is named %s", got)
	}
}

// TestR2Refusals: a bucket whose name R2 would refuse is refused as
// unrealizable, saying which part of the name to shorten; a jurisdiction, which the
// runtime's signed URLs cannot reach, and gcp's versioning are refused as
// settings R2 does not take.
func TestR2Refusals(t *testing.T) {
	reg := assemble(t)
	t.Run("a long name", func(t *testing.T) {
		s := callsStack()
		s.Name = strings.Repeat("calls", 12)
		_, errs := resolveOver(t, reg, s, callsServices(), "Staging")
		refusedWith(t, errs, stack.CodeUnrealizable, "the R2 bucket of shop-media would be named \""+s.Name+"-staging-shop-media\", 79 characters, and R2 takes a name of 3 to 63: "+
			"it joins stack's \""+s.Name+"\" (60), environment's \"staging\" (7), Bucket service's \"shop-media\" (10) with hyphens; shorten the stack's name, the longest")
	})
	t.Run("a long name under a parameter", func(t *testing.T) {
		s := callsStack()
		s.Name = strings.Repeat("c", 52)
		_, errs := resolveOver(t, reg, s, callsServices(), "Preview")
		refusedWith(t, errs, stack.CodeUnrealizable, "the R2 bucket of shop-media would be named with 74 characters before its parameter values, and R2 takes a name of at most 63: "+
			"it joins stack's \""+s.Name+"\" (52), environment's \"preview\" (7), Bucket service's \"shop-media\" (10), parameter pr's \"pr\" (2)")
	})
	for _, setting := range []string{"jurisdiction", "versioning"} {
		t.Run(setting, func(t *testing.T) {
			s := callsStack()
			value := any("eu")
			if setting == "versioning" {
				value = true
			}
			s.Environments[1].Settings[0].Values = map[string]any{setting: value}
			_, errs := resolveOver(t, reg, s, callsServices(), "Staging")
			refusedWith(t, errs, stack.CodeInvalidSettings, "settings of shop-media on platform cloudflare.r2")
		})
	}
}

// TestStackDevKeepsBucketsOnTheEmulator: the fixture's Dev, on the local
// target, keeps the bucket on the storage emulator through the same
// Bucket interface (section 18.13): its edge derives the emulator's
// endpoint, no binding and no R2 credential, and mints no token.
func TestStackDevKeepsBucketsOnTheEmulator(t *testing.T) {
	env := resolveCalls(t, assemble(t), "Dev")
	if p := env.Deployable("shop-media").Platform; strings.HasPrefix(p, "cloudflare.") {
		t.Errorf("Dev places the bucket on %s", p)
	}
	value := derivedValue(t, env, "shop-storefront", "SHOP_MEDIA_BUCKET")
	if !strings.Contains(value, `"endpoint":`) || strings.Contains(value, `"binding"`) || strings.Contains(value, `"r2"`) {
		t.Errorf("Dev's bucket is %s, want the emulator's endpoint alone", value)
	}
	for _, res := range env.Resources.Resources {
		if strings.HasPrefix(res.Type, "cloudflare:") {
			t.Errorf("Dev has Cloudflare node %s", res.ID)
		}
	}
}

// TestPolicyBucketsNeverPublic: an r2.dev managed domain or a custom
// domain on a Bucket service's bucket is refused, whether it names the
// bucket by its node's output or by its name; one on another bucket, and
// the fixture, pass.
func TestPolicyBucketsNeverPublic(t *testing.T) {
	target := newFixture(t).target(t)
	check := target.Policies[slices.IndexFunc(target.Policies, func(r registry.PolicyRule) bool { return r.Name == cloudflare.PolicyBucketsNeverPublic })].Check
	env := resolveCalls(t, assemble(t), "Staging")
	if got := check(env); got != nil {
		t.Errorf("the calls fixture: %v", got)
	}
	for _, res := range []*ir.Resource{
		{ID: "shop-media.public", Type: cloudflare.TypeR2ManagedDomain, Properties: map[string]any{
			"bucketName": ir.Output{Resource: "shop-media.bucket", Name: "name"}, "enabled": true,
		}},
		{ID: "media.domain", Type: cloudflare.TypeR2CustomDomain, Properties: map[string]any{
			"bucketName": "calls-stack-staging-shop-media", "domain": "media.acme.dev",
		}},
		{ID: "other.domain", Type: cloudflare.TypeR2CustomDomain, Properties: map[string]any{
			"bucketName": "a-bucket-of-another-stack", "domain": "assets.acme.dev",
		}},
	} {
		env.Resources.Resources = append(env.Resources.Resources, res)
	}
	got := strings.Join(check(env), "\n")
	for _, want := range []string{
		"the r2.dev managed domain shop-media.public makes the bucket of shop-media public",
		"custom domain media.domain makes the bucket of shop-media public",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the policy does not say %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "\n") != 1 || strings.Contains(got, "other.domain") {
		t.Errorf("the policy found more than the two:\n%s", got)
	}
}

// goneBucketCloud is the fake Cloudflare with buckets that are gone, whose
// objects a list does not find.
type goneBucketCloud struct {
	*fakeCloud
	gone map[string]bool
}

func (c goneBucketCloud) ListObjects(ctx context.Context, account, bucket, prefix string) ([]string, error) {
	if c.gone[bucket] {
		return nil, fmt.Errorf("fake: no bucket %s: %w", bucket, fs.ErrNotExist)
	}
	return c.fakeCloud.ListObjects(ctx, account, bucket, prefix)
}

// putObjects puts each key into bucket on the fake.
func putObjects(t *testing.T, cloud *fakeCloud, bucket string, keys ...string) {
	t.Helper()
	ctx := context.Background()
	if _, err := cloud.EnsureBucket(ctx, accountID, bucket); err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if err := cloud.PutObject(ctx, accountID, bucket, key, []byte(key), nil); err != nil {
			t.Fatal(err)
		}
	}
}

// TestPreDestroyEmptiesAMembersBuckets: before a member of Preview is
// destroyed, the target deletes every object of its bucket, the one named
// with the run's parameter, and leaves every other bucket alone; a plain
// environment's bucket keeps its objects, and a bucket that is gone is
// empty already.
func TestPreDestroyEmptiesAMembersBuckets(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	putObjects(t, f.cloud, "calls-stack-preview-shop-media-pr123", "carts/ada.png", "carts/grace.png")
	putObjects(t, f.cloud, "calls-stack-preview-shop-media-pr124", "carts/alan.png")
	putObjects(t, f.cloud, "calls-stack-staging-shop-media", "carts/staging.png")
	prepare := f.target(t).PreDestroy

	log := &bytes.Buffer{}
	preview := resolveCalls(t, f.reg, "Preview")
	if err := prepare.PrepareDestroy(ctx, registry.Run{Environment: preview, Parameters: map[string]string{"pr": "123"}}, log); err != nil {
		t.Fatal(err)
	}
	left := func(bucket string) []string {
		keys, err := f.cloud.ListObjects(ctx, accountID, bucket, "")
		if err != nil {
			t.Fatal(err)
		}
		return keys
	}
	if keys := left("calls-stack-preview-shop-media-pr123"); len(keys) != 0 {
		t.Errorf("the member's bucket still holds %v", keys)
	}
	if keys := left("calls-stack-preview-shop-media-pr124"); !slices.Equal(keys, []string{"carts/alan.png"}) {
		t.Errorf("another member's bucket holds %v", keys)
	}
	if got := log.String(); got != "  bucket calls-stack-preview-shop-media-pr123: emptied, 2 objects deleted\n" {
		t.Errorf("the log says %q", got)
	}

	staging := resolveCalls(t, f.reg, "Staging")
	if err := prepare.PrepareDestroy(ctx, registry.Run{Environment: staging}, log); err != nil {
		t.Fatal(err)
	}
	if keys := left("calls-stack-staging-shop-media"); !slices.Equal(keys, []string{"carts/staging.png"}) {
		t.Errorf("Staging's bucket holds %v after its destroy was readied", keys)
	}

	// A bucket that is gone is empty.
	gone := goneBucketCloud{fakeCloud: f.cloud, gone: map[string]bool{"calls-stack-preview-shop-media-pr125": true}}
	reg, err := registry.Assemble(registry.DefaultNaming(), &stacktest.Extension{}, cloudflare.Extension{Cloud: gone, Getenv: f.shell.getenv}, pulumiStub{})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := reg.Target(cloudflare.Target)
	log.Reset()
	if err := target.PreDestroy.PrepareDestroy(ctx, registry.Run{Environment: preview, Parameters: map[string]string{"pr": "125"}}, log); err != nil {
		t.Errorf("a bucket that is gone: %v", err)
	}
	if got := log.String(); got != "  bucket calls-stack-preview-shop-media-pr125: gone\n" {
		t.Errorf("the log says %q", got)
	}
}

// TestDestroyEmptiesTheMembersBucketFirst deploys a member of Preview,
// both Workers' bundles built, then destroys it: the destroy empties the
// member's bucket before the provisioner destroys the member, so the
// provider's delete of the bucket finds it empty.
func TestDestroyEmptiesTheMembersBucketFirst(t *testing.T) {
	f := newFixture(t)
	f.shell[cloudflare.PassphraseEnv] = "the-passphrase"
	ctx := context.Background()
	if _, err := f.cloud.EnsureBucket(ctx, accountID, cloudflare.StateBucket); err != nil {
		t.Fatal(err)
	}
	run := registry.Run{Environment: resolveCalls(t, f.reg, "Preview"), Parameters: map[string]string{"pr": "123"}}
	root := t.TempDir()
	for _, server := range []string{"shop-pricing", "shop-storefront"} {
		pkg := filepath.Join(root, "dist", "server", "calls-stack", server)
		if err := os.MkdirAll(pkg, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pkg, "bundle.ts"), []byte("// written by the server generator\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	build := &bundleBuild{version: "v1"}
	log := &bytes.Buffer{}
	options := stack.Options{Registry: f.reg, Run: run, Dir: t.TempDir(), Log: log}
	if _, err := stack.Deploy(ctx, stack.DeployOptions{
		Options: options,
		Sources: &stack.Sources{OutputRoot: filepath.Join(root, "dist"), RepositoryRoot: root, ImplementationRoot: root, Run: build.run},
	}); err != nil {
		t.Fatalf("deploy: %v\n%s", err, log)
	}
	putObjects(t, f.cloud, "calls-stack-preview-shop-media-pr123", "carts/ada.png")

	log.Reset()
	if err := stack.Destroy(ctx, options); err != nil {
		t.Fatalf("destroy: %v\n%s", err, log)
	}
	if keys, _ := f.cloud.ListObjects(ctx, accountID, "calls-stack-preview-shop-media-pr123", ""); len(keys) != 0 {
		t.Errorf("the member's bucket holds %v after its destroy", keys)
	}
	out := log.String()
	emptied, destroyed := strings.Index(out, "bucket calls-stack-preview-shop-media-pr123: emptied, 1 objects deleted"), strings.Index(out, "destroy Preview.pr-123")
	if emptied < 0 || destroyed < 0 || emptied > destroyed {
		t.Errorf("the destroy did not empty the bucket before it destroyed the member:\n%s", out)
	}
	if calls := f.prov.Calls(); calls[len(calls)-1] != "destroy Preview" {
		t.Errorf("the provisioner's last call is %q", calls[len(calls)-1])
	}
}

// TestBootstrapChecksTheBucketTokenGroup: bootstrap of an environment with
// a bucket edge checks that the account lists the permission group the
// edge's token names, by the id Cloudflare publishes and under its name,
// before it applies its graph; an environment with no bucket edge does not
// need it.
func TestBootstrapChecksTheBucketTokenGroup(t *testing.T) {
	f := newFixture(t)
	f.shell[cloudflare.PassphraseEnv] = "the-passphrase"
	ctx := context.Background()
	staging := resolveCalls(t, f.reg, "Staging")
	if _, err := f.target(t).Bootstrap.Bootstrap(ctx, f.request(t, staging, nil, nil)); err != nil {
		t.Fatalf("bootstrap with the group listed: %v", err)
	}

	for _, tc := range []struct {
		name   string
		groups func([]cloudflare.PermissionGroup) []cloudflare.PermissionGroup
		want   string
	}{
		{"missing", func(groups []cloudflare.PermissionGroup) []cloudflare.PermissionGroup {
			return slices.DeleteFunc(groups, func(g cloudflare.PermissionGroup) bool { return g.ID == cloudflare.R2ObjectsWriteGroup })
		}, "list no 2efd5506f9c8494dacb1fa10a3e7d5b6, Workers R2 Storage Bucket Item Write"},
		{"renamed", func(groups []cloudflare.PermissionGroup) []cloudflare.PermissionGroup {
			for i := range groups {
				if groups[i].ID == cloudflare.R2ObjectsWriteGroup {
					groups[i].Name = "Workers R2 Storage Object Write"
				}
			}
			return groups
		}, "group 2efd5506f9c8494dacb1fa10a3e7d5b6 is Workers R2 Storage Object Write"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.shell[cloudflare.PassphraseEnv] = "the-passphrase"
			f.cloud.groups = tc.groups(slices.Clone(permissionGroups))
			if _, err := f.target(t).Bootstrap.Bootstrap(ctx, f.request(t, resolveCalls(t, f.reg, "Staging"), nil, nil)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("bootstrap = %v, want it to say %q", err, tc.want)
			}
			if calls := f.prov.Calls(); slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, "apply") }) {
				t.Errorf("a refused bootstrap applied its graph: %q", calls)
			}
		})
	}
}
