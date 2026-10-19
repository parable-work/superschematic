package generator

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// The Rust deps fixtures (D57): deps-rust-shop, a Rust API whose database
// is its authDb deps-users-db, over whose User table the identity runtime
// authenticates its end users, which calls deps-rust-pricing, lists the
// bucket deps-media (D54) and has a setting and a service clause; and
// deps-rust-pricing, which has none of them, establishes its end users
// itself and verifies a webhook.
const rustDepsGoldenDir = "testdata/rust-deps/golden"

// rustDepsFixtures loads the fixtures' schemas and configs.
func rustDepsFixtures(t *testing.T) (map[string]*ir.Schema, map[string]*schemaconfig.SchemaConfig) {
	t.Helper()
	schemas := map[string]*ir.Schema{}
	configs := map[string]*schemaconfig.SchemaConfig{}
	for _, name := range []string{"deps-users-db", "deps-rust-pricing", "deps-rust-shop"} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(depsFixtures, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		schemas[name], configs[name] = schema, cfg
	}
	return schemas, configs
}

// buildRustDeps runs the fixtures' generators into outputRoot, under the
// repository root repoRoot, scaffolding the implementations there. The
// callee builds first, as the build plan orders it.
func buildRustDeps(t *testing.T, outputRoot, repoRoot string, paths naming.LocalPaths) {
	t.Helper()
	schemas, configs := rustDepsFixtures(t)
	for _, service := range []string{"deps-rust-pricing", "deps-rust-shop"} {
		if _, err := Run(schemas[service], configs[service], Options{
			OutputRoot: outputRoot,
			Paths:      paths,
			Naming:     naming.Default(),
			Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
			LoadDependency: func(name string) (*ir.Schema, error) {
				if schema, ok := schemas[name]; ok {
					return schema, nil
				}
				return nil, fmt.Errorf("no fixture %s", name)
			},
			DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
				cfg, ok := configs[name]
				return cfg, ok
			},
			Stage:              registry.StageAll,
			ImplementationRoot: repoRoot,
			RepositoryRoot:     repoRoot,
		}); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}
}

// rustGoldenPaths are the [paths] of the golden build: checkouts under the
// repository root, so the manifests' relative paths do not depend on the
// machine.
func rustGoldenPaths(repoRoot string) naming.LocalPaths {
	return naming.LocalPaths{
		HTTPRuntimeRust:   filepath.Join(repoRoot, "runtime", "http", "rust"),
		SchemaRuntimeRust: filepath.Join(repoRoot, "runtime", "schema", "rust"),
		ScalarRust:        filepath.Join(repoRoot, "third_party", "superscalar", "crates", "core"),
	}
}

// TestRustDepsAndScaffoldGolden: deps-rust-shop's crate declares Deps with
// its config, a Postgres pool, a client of deps-rust-pricing's Rust SDK,
// the runtime's Bucket for deps-media and its span, Handlers with a field
// per namespace, and Implementations::new, which takes the identity
// authenticator and the service authenticator; its config.rs reads its
// setting, its database, service, bucket, callers, CORS and identity
// fields; its Cargo.toml turns on the
// runtime's postgres, identity-postgres and http-client features and
// depends on the SDK by path. deps-rust-pricing's Handlers hold its
// authenticator and its webhook verifiers, and its config holds its CORS
// field alone, so its crate has no values-schema.json. The scaffold writes each implementation crate. Regenerate
// with:
//
//	go test ./internal/generator -run TestRustDepsAndScaffoldGolden -update
func TestRustDepsAndScaffoldGolden(t *testing.T) {
	repoRoot := t.TempDir()
	outputRoot := filepath.Join(repoRoot, "schemas", "dist")
	buildRustDeps(t, outputRoot, repoRoot, rustGoldenPaths(repoRoot))

	names := naming.Default()
	files := map[string]string{}
	for _, service := range []string{"deps-rust-shop", "deps-rust-pricing"} {
		for _, file := range []string{"Cargo.toml", "src/lib.rs", "src/deps.rs", "src/config.rs"} {
			files[filepath.Join("api", service, file)] = filepath.Join(APIDir(outputRoot, service), filepath.FromSlash(file))
		}
		for _, file := range []string{rustrestgen.ImplementationManifest, rustrestgen.ImplementationLib} {
			files[filepath.Join("rust", service, file)] = filepath.Join(names.RustImplementationDir(repoRoot, service), file)
		}
	}
	files[filepath.Join("api", "deps-rust-shop", "values-schema.json")] = filepath.Join(APIDir(outputRoot, "deps-rust-shop"), "values-schema.json")
	// deps-rust-pricing has no setting and no derived field: its EnvConfig
	// holds its CORS field alone, which values-schema.json never lists.
	if _, err := os.Stat(filepath.Join(APIDir(outputRoot, "deps-rust-pricing"), "values-schema.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("deps-rust-pricing has no setting and no derived field, yet its crate has values-schema.json: %v", err)
	}
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		got, err := os.ReadFile(files[rel])
		if err != nil {
			t.Fatal(err)
		}
		golden := filepath.Join(rustDepsGoldenDir, rel)
		if *updateNamingGolden {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (run with -update to write it)", err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept):\n--- got ---\n%s", rel, got)
		}
	}
}

// TestRustScaffoldNeverOverwrites: a build writes the implementation crate
// only while its directory holds no Cargo.toml, and never touches a crate
// that has one, whether the scaffold's own or another.
func TestRustScaffoldNeverOverwrites(t *testing.T) {
	repoRoot := t.TempDir()
	outputRoot := filepath.Join(repoRoot, "schemas", "dist")
	buildRustDeps(t, outputRoot, repoRoot, rustGoldenPaths(repoRoot))
	dir := naming.Default().RustImplementationDir(repoRoot, "deps-rust-shop")
	lib := filepath.Join(dir, rustrestgen.ImplementationLib)
	edited := []byte("// The engineer's code.\n")
	if err := os.WriteFile(lib, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	buildRustDeps(t, outputRoot, repoRoot, rustGoldenPaths(repoRoot))
	if got, err := os.ReadFile(lib); err != nil || string(got) != string(edited) {
		t.Fatalf("a second build rewrote %s: %q, %v", lib, got, err)
	}

	if err := os.Remove(lib); err != nil {
		t.Fatal(err)
	}
	buildRustDeps(t, outputRoot, repoRoot, rustGoldenPaths(repoRoot))
	if _, err := os.Stat(lib); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a build scaffolded into a crate that holds a Cargo.toml: %v", err)
	}
}

// TestRustDepsRefusals: Deps holds a Rust SDK client per call, so a callee
// whose config the build has and that generates no Rust SDK is refused,
// naming the switch to turn on; and a bucket whose Deps field would take
// another field's name is refused (D54).
func TestRustDepsRefusals(t *testing.T) {
	run := func(schemas map[string]*ir.Schema, configs map[string]*schemaconfig.SchemaConfig) error {
		_, err := Run(schemas["deps-rust-shop"], configs["deps-rust-shop"], Options{
			OutputRoot:     t.TempDir(),
			Paths:          rustGoldenPaths(t.TempDir()),
			LoadDependency: func(name string) (*ir.Schema, error) { return schemas[name], nil },
			DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
				c, ok := configs[name]
				return c, ok
			},
		})
		return err
	}

	schemas, configs := rustDepsFixtures(t)
	noSDK := *configs["deps-rust-pricing"]
	noSDK.Outputs = map[string]any{"types": map[string]any{"rust": map[string]any{"enabled": true}}, "api": map[string]any{"enabled": true, "language": APILanguageRust}}
	configs["deps-rust-pricing"] = &noSDK
	if err := run(schemas, configs); err == nil || !strings.Contains(err.Error(), "the Rust server of deps-rust-shop holds a client of deps-rust-pricing in its Deps, and deps-rust-pricing generates none; enable outputs.sdk.rust in deps-rust-pricing's config") {
		t.Errorf("Run = %v, want the refusal of a callee without a Rust SDK", err)
	}

	schemas, configs = rustDepsFixtures(t)
	schemas["deps-rust-shop"].Buckets = append(schemas["deps-rust-shop"].Buckets, ir.ServiceRef{Name: "deps-rust-pricing", Kind: ir.SchemaKindBucket})
	if err := run(schemas, configs); err == nil || !strings.Contains(err.Error(), "deps-rust-shop lists bucket deps-rust-pricing, which would be Deps.deps_rust_pricing, the field of deps-rust-pricing") {
		t.Errorf("Run = %v, want the refusal of a bucket whose field is a client's", err)
	}
}

// rustDepsTest is tests/deps.rs of deps-rust-shop's implementation crate:
// it builds the implementation as the entrypoint does, from the
// configuration load_env_config reads (env, the variables
// ir.DerivedVariables makes), a lazy Postgres pool, a client of
// deps-rust-pricing with its credential source and deps-media on GCS
// (D54), which opening does not reach, joins the handlers with
// the identity and service authenticators, and calls the router: an
// operation anyone may call answers 501, and one that needs a caller 401.
// Before the variables are set, load_env_config names each one missing.
const rustDepsTest = `use std::sync::Arc;

use axum::body::Body;
use axum::http::{Request, StatusCode};
use schemas_deps_rust_pricing_sdk::{ClientConfig, DepsRustPricingSdk, ServiceCredential};
use schemas_deps_rust_shop_api::config::{load_env_config, EnvConfig};
use schemas_deps_rust_shop_api::runtime::identity::{IdentityAuthenticator, PostgresPool};
use schemas_deps_rust_shop_api::runtime::bucket::Bucket;
use schemas_deps_rust_shop_api::runtime::stackconfig::{self, Database};
use schemas_deps_rust_shop_api::runtime::{gcs, postgres, JwtServiceAuthenticator};
use schemas_deps_rust_shop_api::{build_router, identity, Deps, Handlers, Implementations};
use schemas_deps_rust_shop_implementation::create;
use tower::ServiceExt;

const ENV: &[(&str, &str)] = &[
ENV_PAIRS];

async fn implementations(config: EnvConfig) -> Implementations {
    let pool = postgres::connect(&config.deps_users_db_database).expect("the pool opens lazily");
    let endpoint = &config.deps_rust_pricing_service;
    let service_credential = endpoint.credential.as_ref().map(|credential| ServiceCredential {
        token: stackconfig::credential_source(credential).expect("the credential source builds"),
        headers: credential.header_names(),
    });
    let pricing = DepsRustPricingSdk::new(ClientConfig {
        base_url: endpoint.url.clone(),
        service_credential,
        ..ClientConfig::default()
    })
    .expect("the client builds");
    let media = gcs::open_bucket(&config.deps_media_bucket).expect("the bucket opens");
    assert_eq!(media.name(), "acme-shop-deps-media");
    let deps = Deps {
        config: config.clone(),
        db: pool.clone(),
        deps_rust_pricing: pricing,
        deps_media: Arc::new(media),
        span: tracing::info_span!("api", name = "deps-rust-shop"),
    };
    let handlers: Handlers = create(deps).await.expect("create builds the handlers");
    let store = identity::store(PostgresPool::new(pool)).expect("the store builds");
    let service = identity::service(Arc::new(store), config.deps_rust_shop_identity.clone())
        .expect("the identity service builds");
    let authenticator = Arc::new(IdentityAuthenticator::new(Arc::new(service)));
    let service_authenticator = Arc::new(
        JwtServiceAuthenticator::new(config.deps_rust_shop_callers.clone())
            .expect("the service authenticator builds"),
    );
    Implementations::new(handlers, authenticator, service_authenticator)
}

#[tokio::test]
async fn the_implementation_builds_from_deps_and_answers_501() {
    let missing = load_env_config().expect_err("nothing is set").to_string();
    for variable in [
        "DEPS_USERS_DB_DATABASE_URL",
        "DEPS_RUST_PRICING_SERVICE_URL",
        "DEPS_MEDIA_BUCKET_NAME",
        "DEPS_RUST_SHOP_CALLERS_ISSUERS",
    ] {
        assert!(missing.contains(variable), "{missing}");
    }

    for (name, value) in ENV {
        std::env::set_var(name, value);
    }
    let config = load_env_config().expect("the configuration loads");
    assert_eq!(config.storefront_region, "eu");
    let Database::CloudSql(cloud_sql) = &config.deps_users_db_database else {
        panic!("{:?}", config.deps_users_db_database);
    };
    assert_eq!(cloud_sql.instance, "acme:us-central1:shop");
    assert_eq!(config.deps_rust_pricing_service.url, "https://deps-rust-pricing.run.app");
    assert_eq!(config.deps_media_bucket.name, "acme-shop-deps-media");
    assert_eq!(config.deps_media_bucket.endpoint, None);
    assert!(config.deps_rust_shop_callers.issuers.is_empty());
    assert_eq!(config.deps_rust_shop_cors.origins, ["https://shop.example.com"]);
    assert!(!config.deps_rust_shop_identity.cookie.secure);

    let router = build_router(implementations(config).await);
    let products = router
        .clone()
        .oneshot(Request::get("/api/products").body(Body::empty()).unwrap())
        .await
        .unwrap();
    assert_eq!(products.status(), StatusCode::NOT_IMPLEMENTED);
    let cart = router
        .oneshot(Request::get("/api/carts/c-1").body(Body::empty()).unwrap())
        .await
        .unwrap();
    assert_eq!(cart.status(), StatusCode::UNAUTHORIZED);
}
`

// rustPricingTest is tests/deps.rs of deps-rust-pricing's implementation
// crate: the handlers create returns, with the authenticator the
// implementation establishes its end users with, make the API's
// Implementations, which lack a verifier for the webhook's provider until
// the implementation adds one.
const rustPricingTest = `use schemas_deps_rust_pricing_api::config::load_env_config;
use schemas_deps_rust_pricing_api::{Deps, Implementations};
use schemas_deps_rust_pricing_implementation::create;

#[tokio::test]
async fn the_handlers_make_the_implementations() {
    let deps = Deps {
        config: load_env_config().expect("the configuration loads"),
        span: tracing::Span::none(),
    };
    let implementations = Implementations::new(create(deps).await.expect("create builds the handlers"));
    let refusal = implementations.validate_implementations().expect_err("no webhook verifier");
    assert!(refusal.contains("github"), "{refusal}");
}
`

// TestRustDepsAndScaffoldCompile: the generated API crates, each with its
// Deps and its config, and the scaffolded implementation crates, which
// depend on them by path, pass cargo clippy with warnings denied; and the
// shop's implementation builds from Deps as the entrypoint builds it,
// joins the authenticators through Implementations::new and answers 501
// (rustDepsTest), while the pricing API's makes its Implementations from
// its handlers alone (rustPricingTest).
func TestRustDepsAndScaffoldCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust build")
	}
	paths := testpaths.Local(t)
	repoRoot := testpaths.TempDir(t)
	outputRoot := filepath.Join(repoRoot, "schemas", "dist")
	buildRustDeps(t, outputRoot, repoRoot, paths)

	var pairs strings.Builder
	for field, value := range map[string]any{
		"DEPS_USERS_DB_DATABASE": ir.DatabaseConnection{CloudSQL: &ir.CloudSQLConnection{
			Instance: "acme:us-central1:shop", Database: "deps_users_db", User: "deps-rust-shop@acme.iam",
		}},
		"DEPS_RUST_PRICING_SERVICE": ir.ServiceEndpoint{URL: "https://deps-rust-pricing.run.app", Credential: &ir.ServiceCredential{
			Source: ir.CredentialGoogleIDToken, Audience: "https://deps-rust-pricing.run.app",
		}},
		"DEPS_MEDIA_BUCKET": ir.BucketConnection{Name: "acme-shop-deps-media"},
	} {
		vars, err := ir.DerivedVariables(field, value)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range vars {
			fmt.Fprintf(&pairs, "    (%q, %q),\n", v.Name, v.Value)
		}
	}
	for name, value := range map[string]string{
		"DEPS_RUST_SHOP_CALLERS_ISSUERS": "0",
		"DEPS_RUST_SHOP_CORS_ORIGINS":    "https://shop.example.com",
		"DEPS_RUST_SHOP_IDENTITY":        `{"cookie":{"secure":false}}`,
	} {
		fmt.Fprintf(&pairs, "    (%q, %q),\n", name, value)
	}

	names := naming.Default()
	devDependencies := `
[dev-dependencies]
axum = "0.8.9"
tokio = { version = "1.52.2", features = ["macros", "rt-multi-thread"] }
tower = { version = "0.5", features = ["util"] }
tracing = { version = "` + rustrestgen.TracingVersion + `", default-features = false, features = ["std"] }
`
	for service, test := range map[string]string{
		"deps-rust-shop":    strings.Replace(rustDepsTest, "ENV_PAIRS", pairs.String(), 1),
		"deps-rust-pricing": rustPricingTest,
	} {
		dir := names.RustImplementationDir(repoRoot, service)
		manifest := filepath.Join(dir, rustrestgen.ImplementationManifest)
		cargoToml, err := os.ReadFile(manifest)
		if err != nil {
			t.Fatal(err)
		}
		// The shop's test builds the client of deps-rust-pricing, as the
		// entrypoint does, from the SDK crate the API crate depends on, and
		// opens deps-media on GCS with the runtime's gcs feature, as the
		// entrypoint turns it on.
		dev := devDependencies
		if service == "deps-rust-shop" {
			sdk, err := naming.RelPath(dir, SDKDir(outputRoot, LangRust, "deps-rust-pricing"))
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := naming.RelPath(dir, paths.HTTPRuntimeRust)
			if err != nil {
				t.Fatal(err)
			}
			dev += names.RustSDKCrate("deps-rust-pricing") + ` = { path = "` + sdk + `" }` + "\n"
			dev += names.HTTPRuntimeRustCrate + ` = { path = "` + runtime + `", features = ["gcs"] }` + "\n"
		}
		if err := os.WriteFile(manifest, append(cargoToml, dev...), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tests", "deps.rs"), []byte(test), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	targetDir := os.Getenv("CARGO_TARGET_DIR")
	if targetDir == "" {
		targetDir = filepath.Join(t.TempDir(), "target")
	}
	cargo := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(cargoPath, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+targetDir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cargo %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
		}
	}
	clippy := []string{"clippy", "--quiet", "--all-targets", "--", "-D", "warnings"}
	for _, service := range []string{"deps-rust-pricing", "deps-rust-shop"} {
		cargo(APIDir(outputRoot, service), clippy...)
		dir := names.RustImplementationDir(repoRoot, service)
		cargo(dir, clippy...)
		cargo(dir, "test", "--quiet")
	}
}
