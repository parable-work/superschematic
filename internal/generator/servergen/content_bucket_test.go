package servergen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/generator/tsrestgen"
	ir "github.com/parable-work/superschematic/ir"
)

// TestGoContentAddressedBucketWiring: with shop-media's config saying
// contentAddressed, each API that lists it holds a *bucket.ContentBucket
// in its Deps, which Storefront's main.go builds over the one bucket it
// opens, with the config's maxBytes; buckets.go holds contentBucket either
// way (D142).
func TestGoContentAddressedBucketWiring(t *testing.T) {
	read := func(repoRoot string, parts ...string) string {
		data, err := os.ReadFile(filepath.Join(append([]string{repoRoot, "schemas", "dist"}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	plain := t.TempDir()
	f := loadFixture(t, servicesRoot)
	f.build(t, plain, fakePaths(plain))
	if deps := read(plain, "api", "shop-reviews", "deps.go"); !strings.Contains(deps, "\tShopMedia bucket.Bucket\n") {
		t.Errorf("shop-reviews' Deps of a plain bucket:\n%s", deps)
	}
	if buckets := read(plain, "server", "shop-stack", "Storefront", servergen.BucketsFile); !strings.Contains(buckets, "func contentBucket(raw bucket.Bucket, maxBytes int64) *bucket.ContentBucket {") ||
		!strings.Contains(buckets, "object = object.If(storage.Conditions{DoesNotExist: true})") ||
		!strings.Contains(buckets, "func (b *gcsBucket) Head(ctx context.Context, name string) (bucket.Object, bool, error) {") {
		t.Errorf("buckets.go lacks the content bucket, the put if absent or the head:\n%s", buckets)
	}

	content := t.TempDir()
	f = loadFixture(t, servicesRoot)
	f.configs["shop-media"].ContentAddressed = true
	f.configs["shop-media"].MaxBytes = 1 << 20
	f.build(t, content, fakePaths(content))
	for _, api := range []string{"shop-orders", "shop-reviews"} {
		deps := read(content, "api", api, "deps.go")
		if !strings.Contains(deps, "\tShopMedia *bucket.ContentBucket\n") || !strings.Contains(deps, "1048576 bytes under the SHA-256 of its bytes") || strings.Contains(deps, "ShopMedia bucket.Bucket") {
			t.Errorf("%s's Deps of a content-addressed bucket:\n%s", api, deps)
		}
	}
	main := read(content, "server", "shop-stack", "Storefront", servergen.MainFile)
	if got := strings.Count(main, "openBucket(ctx, \"SHOP_MEDIA_BUCKET\""); got != 1 {
		t.Errorf("Storefront opens shop-media %d times, want once:\n%s", got, main)
	}
	if got := strings.Count(main, "ShopMedia: contentBucket(shopMediaBucket, 1048576),"); got != 2 {
		t.Errorf("Storefront hands %d APIs a content bucket, want 2:\n%s", got, main)
	}

	// Without maxBytes the bucket puts at most 32 MiB.
	byDefault := t.TempDir()
	f = loadFixture(t, servicesRoot)
	f.configs["shop-media"].ContentAddressed = true
	f.build(t, byDefault, fakePaths(byDefault))
	if main := read(byDefault, "server", "shop-stack", "Storefront", servergen.MainFile); !strings.Contains(main, "ShopMedia: contentBucket(shopMediaBucket, 33554432),") {
		t.Errorf("a content-addressed bucket with no maxBytes:\n%s", main)
	}
}

// TestTypeScriptContentAddressedBucketWiring: a TypeScript server's
// entrypoint, on Node.js and on Workers, hands Deps a content bucket over
// the bucket it opens, on GCS or on the R2 binding, and imports
// contentBucket from the HTTP runtime for it alone (D142).
func TestTypeScriptContentAddressedBucketWiring(t *testing.T) {
	for _, workers := range []bool{false, true} {
		for _, contentAddressed := range []bool{false, true} {
			bucket := tsrestgen.DepsBucket{Service: "shop-media", Field: "media"}
			if contentAddressed {
				bucket.ContentAddressed, bucket.MaxBytes = true, 1<<20
			}
			in := servergen.TypeScriptInput{
				Stack:   "edge-stack",
				Server:  "shop-api",
				Dir:     t.TempDir(),
				Workers: workers,
				APIs: []servergen.TypeScriptAPIInput{{
					Output: &tsrestgen.APIOutput{
						SchemaName:   "shop-api",
						PackageName:  "@schemas/shop-api-api",
						HasEnvConfig: true,
						Endpoints:    []tsrestgen.EndpointInfo{{Name: "getCart"}},
						Deps:         tsrestgen.DepsInfo{Buckets: []tsrestgen.DepsBucket{bucket}},
					},
					Routes: &apigen.APIOutput{SchemaName: "shop-api"},
					Config: &envgen.ConfigOutput{
						SchemaName: "shop-api",
						Derived:    []envgen.DerivedField{{Key: "SHOP_MEDIA_BUCKET", Kind: ir.EdgeBucket, Service: "shop-media"}},
					},
					Implementation: servergen.TypeScriptImplementation{Package: "@schemas/shop-api-implementation"},
				}},
			}
			s, err := servergen.PlanTypeScript(in)
			if err != nil {
				t.Fatal(err)
			}
			if err := servergen.WriteTypeScript(s, in.Dir); err != nil {
				t.Fatal(err)
			}
			file := servergen.TypeScriptMainFile
			if workers {
				file = servergen.TypeScriptFetchFile
			}
			raw, err := os.ReadFile(filepath.Join(in.Dir, file))
			if err != nil {
				t.Fatal(err)
			}
			entry := string(raw)
			wired := strings.Contains(entry, "    media: contentBucket(shopMediaBucket, { maxBytes: 1048576 }),\n")
			plain := strings.Contains(entry, "    media: shopMediaBucket,\n")
			imported := strings.Contains(entry, "contentBucket, ") || strings.Contains(entry, ", contentBucket }")
			if wired != contentAddressed || plain == contentAddressed || imported != contentAddressed {
				t.Errorf("workers %t, contentAddressed %t: wired %t, plain %t, imported %t:\n%s", workers, contentAddressed, wired, plain, imported, entry)
			}
		}
	}
}
