package cloudflare_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// The cloudflare target has no memory store platform (D75): a stack that
// places a store there with it on is refused, and one that turns it off
// deploys, its Workers opening the store disabled.

var shopCache = ir.ServiceRef{Name: "shop-cache", Kind: ir.SchemaKindMemoryStore}

// cacheServices are AcmeShop's services with shop-storefront listing the
// MemoryStore service shop-cache.
func cacheServices() []stack.Service {
	services := append(stacktest.AcmeShop(), stack.Service{Name: shopCache.Name, Kind: shopCache.Kind})
	for i := range services {
		if services[i].Name == stacktest.ShopStorefront.Name {
			services[i].MemoryStores = []ir.ServiceRef{shopCache}
		}
	}
	return services
}

// cacheOff returns the storefront's stack with shop-cache turned off in
// Staging.
func cacheOff() *ir.Stack {
	s := storefrontStack()
	off := false
	staging := s.Environment("Staging")
	staging.Settings = append(staging.Settings, &ir.DeployableSettings{Of: stacktest.Of(shopCache), Enabled: &off})
	return s
}

// TestAMemoryStoreIsRefused: the storefront's Worker lists shop-cache, so
// a Staging that leaves the store on is refused, and the refusal names
// `enabled: false` as the way to run without it.
func TestAMemoryStoreIsRefused(t *testing.T) {
	reg := assemble(t)
	if _, ok := reg.Platform("cloudflare.memorystore"); ok {
		t.Fatal("cloudflare registers a memory store platform")
	}
	target, _ := reg.Target(cloudflare.Target)
	if platform, ok := target.Platforms[ir.DeployableMemoryStore]; ok {
		t.Fatalf("cloudflare places memory stores on %s", platform)
	}
	_, errs := resolveOver(t, reg, storefrontStack(), cacheServices(), "Staging")
	refusedWith(t, errs, stack.CodeUnrealizable, "memoryStore shop-cache: target cloudflare has no platform for a memory store")
	refusedWith(t, errs, stack.CodeUnrealizable, "turn it off in this environment with `settings: [{ of: ShopCache, enabled: false }]`")
}

// TestAMemoryStoreTurnedOffDeploys: with `enabled: false` the storefront's
// Staging resolves with the store in Disabled, no node, no edge and no
// binding for it, and deploys: its Worker's version holds no variable of
// the store, so the Worker opens it disabled.
func TestAMemoryStoreTurnedOffDeploys(t *testing.T) {
	f := newFixture(t)
	env, errs := resolveOver(t, f.reg, cacheOff(), cacheServices(), "Staging")
	if errs != nil {
		t.Fatal(errs)
	}
	want := []*ir.DisabledDeployable{{Name: "shop-cache", Kind: ir.DeployableMemoryStore, Services: []ir.ServiceRef{shopCache}, From: "Staging"}}
	if got, w := jsonOf(t, env.Disabled), jsonOf(t, want); got != w {
		t.Errorf("disabled = %s, want %s", got, w)
	}
	if env.Deployable("shop-cache") != nil || len(env.Deployable("shop-storefront").MemoryStores) != 0 {
		t.Error("a store turned off is placed, or the storefront reaches it")
	}
	for _, e := range env.Edges {
		if e.Kind == ir.EdgeMemoryStore {
			t.Errorf("edge %s reaches a store turned off", e.ID)
		}
	}
	for _, b := range env.Deployable("shop-storefront").Bindings {
		if strings.HasPrefix(b.Field, "SHOP_CACHE") {
			t.Errorf("the storefront binds %s", b.Field)
		}
	}

	f.shell[cloudflare.PassphraseEnv] = "the-passphrase"
	ctx := context.Background()
	if _, err := f.cloud.EnsureBucket(ctx, accountID, cloudflare.StateBucket); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	pkg := filepath.Join(root, "dist", "server", "storefront-stack", "shop-storefront")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "bundle.ts"), []byte("// written by the server generator\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := &bytes.Buffer{}
	build := &bundleBuild{version: "v1"}
	if _, err := stack.Deploy(ctx, stack.DeployOptions{
		Options: stack.Options{Registry: f.reg, Run: registry.Run{Environment: env}, Dir: t.TempDir(), Log: log},
		Sources: &stack.Sources{OutputRoot: filepath.Join(root, "dist"), RepositoryRoot: root, ImplementationRoot: root, Run: build.run},
	}); err != nil {
		t.Fatalf("deploy: %v\n%s", err, log)
	}
	rendered := f.prov.Rendered()
	for _, res := range rendered.Resources.Resources {
		if strings.Contains(res.ID, "shop-cache") {
			t.Errorf("the deploy rendered %s", res.ID)
		}
	}
	version := rendered.Resources.Resource("shop-storefront.version")
	if version == nil {
		t.Fatal("the deploy rendered no version of the storefront")
	}
	if got := jsonOf(t, version.Properties); strings.Contains(got, "SHOP_CACHE") {
		t.Errorf("the storefront's version holds a variable of the store: %s", got)
	}
}
