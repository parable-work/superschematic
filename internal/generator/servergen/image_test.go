package servergen_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/stackdeploy"
	ir "github.com/parable-work/superschematic/ir"
)

// imageGoldenRoot holds the Dockerfile and the ignore file of each
// entrypoint whose image the stack adds to (D144), laid out as the output
// root lays them out, under a directory per language.
const imageGoldenRoot = "testdata/golden/images"

// withImages gives the fixture's stack the images elements given, as its
// `@stack` would list them.
func (f fixture) withImages(t *testing.T, stack string, images ...*ir.DeployableImage) {
	t.Helper()
	for _, td := range f.schemas[stack].Types {
		if td.Stack != nil {
			td.Stack.Images = images
			return
		}
	}
	t.Fatalf("fixture %s declares no @stack class", stack)
}

// dockerFiles lists the Dockerfile and the ignore file of each of
// servers, by their path under imageGoldenRoot.
func dockerFiles(repoRoot, language, stack string, servers ...string) map[string]string {
	files := map[string]string{}
	out := filepath.Join(repoRoot, "schemas", "dist")
	for _, server := range servers {
		for _, file := range []string{servergen.DockerFile, servergen.DockerIgnoreFile} {
			files[filepath.Join(language, stack, server, file)] = filepath.Join(servergen.ServerDir(out, stack, server), file)
		}
	}
	return files
}

// writeExtension writes a Dockerfile of the project's that extends a
// generated image, with a file beside it that it copies.
func writeExtension(t *testing.T, repoRoot, path string) {
	t.Helper()
	file := filepath.Join(repoRoot, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("ARG BASE\nFROM ${BASE}\nCOPY "+filepath.ToSlash(filepath.Dir(path))+"/invoice.ttf /usr/share/fonts/invoice.ttf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(file), "invoice.ttf"), []byte("a font"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readDocker reads a generated Dockerfile and its ignore file.
func readDocker(t *testing.T, repoRoot, stack, server string) (dockerfile, ignore string) {
	t.Helper()
	dir := servergen.ServerDir(filepath.Join(repoRoot, "schemas", "dist"), stack, server)
	for i, file := range []string{servergen.DockerFile, servergen.DockerIgnoreFile} {
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			dockerfile = string(data)
		} else {
			ignore = string(data)
		}
	}
	return dockerfile, ignore
}

// wantAll fails for each of wants that text does not hold.
func wantAll(t *testing.T, what, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("%s holds no %q:\n%s", what, want, text)
		}
	}
}

// TestGoImageAdditions (D144): a Go entrypoint whose image the stack adds
// packages to runs on Debian's slim image in distroless's place, since
// distroless has no apt-get: its final stage installs the stack's packages
// and what distroless held, sorted, removes the package lists, and runs
// the binary as the same non-root user. One the stack extends keeps
// distroless, names the extending Dockerfile in its header, and takes that
// file and its directory into its build context. An entrypoint the stack
// names nothing for is written as before. Regenerate with:
//
//	go test ./internal/generator/servergen -run ImageAdditions -update
func TestGoImageAdditions(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	api := ir.ServiceRef{Name: "shop-api", Kind: ir.SchemaKindAPI}
	orders := ir.ServiceRef{Name: "shop-orders", Kind: ir.SchemaKindAPI}
	f.withImages(t, cloudStack,
		&ir.DeployableImage{Of: ir.DeployableRef{Service: &api}, Packages: []string{"fonts-liberation", "chromium"}},
		&ir.DeployableImage{Of: ir.DeployableRef{Service: &orders, Worker: "FulfilOrders"}, Extend: "docker/fulfil/Dockerfile"},
		&ir.DeployableImage{Of: ir.DeployableRef{Service: &orders, Job: "ExpireOrders"}, Packages: []string{"poppler-utils"}, Extend: "docker/fulfil/Dockerfile"},
	)
	writeExtension(t, repoRoot, "docker/fulfil/Dockerfile")
	f.build(t, repoRoot, fakePaths(repoRoot), append(slices.Clone(apis), cloudStack)...)
	compareGoldens(t, imageGoldenRoot, dockerFiles(repoRoot, "go", cloudStack, "shop-api", fulfilOrders, expireOrders))

	dockerfile, ignore := readDocker(t, repoRoot, cloudStack, "shop-api")
	wantAll(t, "shop-api's Dockerfile", dockerfile,
		"\nFROM debian:trixie-slim\nRUN apt-get update \\\n    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \\\n"+
			"      ca-certificates \\\n      chromium \\\n      fonts-liberation \\\n      openssl \\\n      tzdata \\\n    && rm -rf /var/lib/apt/lists/* \\\n",
		"    && groupadd --gid 65532 nonroot \\\n    && useradd --uid 65532 --gid 65532 --create-home nonroot\nCOPY --from=build /out/server /server\nUSER nonroot\nWORKDIR /home/nonroot\n",
		"#   chromium fonts-liberation\n")
	if strings.Contains(dockerfile, "FROM gcr.io/distroless") {
		t.Errorf("shop-api's Dockerfile, which installs packages, still runs on distroless:\n%s", dockerfile)
	}
	if strings.Contains(ignore, "docker") {
		t.Errorf("shop-api's context, which nothing extends, takes in the extension's directory:\n%s", ignore)
	}

	dockerfile, ignore = readDocker(t, repoRoot, cloudStack, fulfilOrders)
	wantAll(t, "the worker's Dockerfile", dockerfile,
		"\nFROM gcr.io/distroless/cc-debian13:nonroot\n",
		"#   docker build -f docker/fulfil/Dockerfile --build-arg BASE=<the image this file built> .\n")
	if strings.Contains(dockerfile, "apt-get") {
		t.Errorf("the worker's Dockerfile, which names no packages, installs some:\n%s", dockerfile)
	}
	wantAll(t, "the worker's ignore file", ignore, "\n!docker/fulfil\n", "\n!docker/fulfil/Dockerfile\n")

	dockerfile, _ = readDocker(t, repoRoot, cloudStack, expireOrders)
	wantAll(t, "the job's Dockerfile", dockerfile, "      poppler-utils \\\n", "--build-arg BASE=", "\nFROM debian:trixie-slim\n", "COPY --from=build /out/job /job\nUSER nonroot\nWORKDIR /home/nonroot\n")

	// Storefront, which the stack names nothing for, is as shop-stack's
	// build writes it.
	dockerfile, _ = readDocker(t, repoRoot, cloudStack, "Storefront")
	if strings.Contains(dockerfile, "apt-get") || strings.Contains(dockerfile, "BASE") || !strings.Contains(dockerfile, "\nFROM gcr.io/distroless/cc-debian13:nonroot\n") {
		t.Errorf("Storefront's Dockerfile, which the stack adds nothing to, changed:\n%s", dockerfile)
	}
}

// TestImageRefusals (D144): the stack's build refuses an image element
// that names a Dockerfile that is no file, one that is a directory, a
// deployable that runs no image, a deployable twice, a package that is no
// Debian package name, and an element that adds nothing.
func TestImageRefusals(t *testing.T) {
	api := ir.ServiceRef{Name: "shop-api", Kind: ir.SchemaKindAPI}
	db := ir.ServiceRef{Name: "shop-db", Kind: ir.SchemaKindDB}
	orders := ir.ServiceRef{Name: "shop-orders", Kind: ir.SchemaKindAPI}
	for name, c := range map[string]struct {
		images []*ir.DeployableImage
		want   string
	}{
		"a Dockerfile that is no file": {
			[]*ir.DeployableImage{{Of: ir.DeployableRef{Service: &api}, Extend: "docker/missing/Dockerfile"}},
			"server shop-api: the stack extends its image with docker/missing/Dockerfile, which is no file at ",
		},
		"a directory": {
			[]*ir.DeployableImage{{Of: ir.DeployableRef{Service: &api}, Extend: "docker"}},
			"server shop-api: the stack extends its image with docker, and ",
		},
		"a database": {
			[]*ir.DeployableImage{{Of: ir.DeployableRef{Service: &db}, Packages: []string{"chromium"}}},
			"images[0] (of shop-db) names database shop-db, which runs no image",
		},
		"a deployable twice": {
			[]*ir.DeployableImage{{Of: ir.DeployableRef{Service: &api}, Packages: []string{"chromium"}}, {Of: ir.DeployableRef{Service: &api}, Packages: []string{"tini"}}},
			"images[0] and images[1] both name server shop-api",
		},
		"a job the API does not declare": {
			[]*ir.DeployableImage{{Of: ir.DeployableRef{Service: &orders, Job: "PrintLabels"}, Packages: []string{"chromium"}}},
			"names job PrintLabels of shop-orders, which declares no such @job class",
		},
		"a package with a version": {
			[]*ir.DeployableImage{{Of: ir.DeployableRef{Service: &api}, Packages: []string{"chromium=120.0"}}},
			`package "chromium=120.0" is no Debian package name`,
		},
		"a package with a shell word": {
			[]*ir.DeployableImage{{Of: ir.DeployableRef{Service: &api}, Packages: []string{"curl && rm -rf /"}}},
			`is no Debian package name`,
		},
		"a path outside the repository": {
			[]*ir.DeployableImage{{Of: ir.DeployableRef{Service: &api}, Extend: "../Dockerfile"}},
			`extend is "../Dockerfile"; it is the path of a Dockerfile under the repository root`,
		},
		"nothing": {
			[]*ir.DeployableImage{{Of: ir.DeployableRef{Service: &api}}},
			"names no packages and no Dockerfile to extend with",
		},
	} {
		t.Run(name, func(t *testing.T) {
			repoRoot := t.TempDir()
			if err := os.MkdirAll(filepath.Join(repoRoot, "docker"), 0o755); err != nil {
				t.Fatal(err)
			}
			f := loadFixture(t, servicesRoot)
			f.withImages(t, cloudStack, c.images...)
			f.build(t, repoRoot, fakePaths(repoRoot), apis...)
			_, err := generator.Run(f.schemas[cloudStack], f.configs[cloudStack], f.options(repoRoot, fakePaths(repoRoot)))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want one saying %q", err, c.want)
			}
		})
	}
}

// TestTypeScriptImageAdditions (D144): a TypeScript entrypoint's image is
// Bun's, a Debian one, so its final stage installs the stack's packages
// there, before it leaves root, and nothing beside them; one the stack
// extends takes the extending Dockerfile and its directory into its
// context.
func TestTypeScriptImageAdditions(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTypeScriptFixture(t)
	pricing := ir.ServiceRef{Name: "ts-pricing", Kind: ir.SchemaKindAPI}
	shop := ir.ServiceRef{Name: "ts-shop", Kind: ir.SchemaKindAPI}
	f.withImages(t, tsCloudStack,
		&ir.DeployableImage{Of: ir.DeployableRef{Service: &pricing}, Packages: []string{"fonts-liberation", "chromium"}},
		&ir.DeployableImage{Of: ir.DeployableRef{Service: &shop, Worker: "RemindShoppers"}, Extend: "docker/remind/Dockerfile"},
	)
	writeExtension(t, repoRoot, "docker/remind/Dockerfile")
	f.build(t, repoRoot, tsFakePaths(repoRoot), append(slices.Clone(tsAPIs), tsCloudStack)...)
	compareGoldens(t, imageGoldenRoot, dockerFiles(repoRoot, "typescript", tsCloudStack, "ts-pricing", tsRemindShoppers))

	dockerfile, _ := readDocker(t, repoRoot, tsCloudStack, "ts-pricing")
	wantAll(t, "ts-pricing's Dockerfile", dockerfile,
		"\nFROM oven/bun:${BUN_VERSION}\nRUN apt-get update \\\n    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \\\n"+
			"      chromium \\\n      fonts-liberation \\\n    && rm -rf /var/lib/apt/lists/*\nCOPY --from=build ",
		"#   chromium fonts-liberation\n", "\nUSER bun\n")
	if strings.Contains(dockerfile, "ca-certificates") || strings.Contains(dockerfile, "useradd") {
		t.Errorf("ts-pricing's Dockerfile installs what Bun's image already holds:\n%s", dockerfile)
	}
	if strings.Index(dockerfile, "apt-get install") > strings.Index(dockerfile, "\nUSER bun\n") {
		t.Errorf("ts-pricing's Dockerfile installs its packages after it leaves root:\n%s", dockerfile)
	}

	dockerfile, ignore := readDocker(t, repoRoot, tsCloudStack, tsRemindShoppers)
	wantAll(t, "the worker's Dockerfile", dockerfile, "#   docker build -f docker/remind/Dockerfile --build-arg BASE=<the image this file built> .\n")
	if strings.Contains(dockerfile, "apt-get") {
		t.Errorf("the worker's Dockerfile, which names no packages, installs some:\n%s", dockerfile)
	}
	wantAll(t, "the worker's ignore file", ignore, "\n!docker/remind\n", "\n!docker/remind/Dockerfile\n")

	dockerfile, _ = readDocker(t, repoRoot, tsCloudStack, "ts-shop")
	if strings.Contains(dockerfile, "apt-get") || strings.Contains(dockerfile, "BASE=") {
		t.Errorf("ts-shop's Dockerfile, which the stack adds nothing to, changed:\n%s", dockerfile)
	}
}

// TestRustImageAdditions (D144): a Rust entrypoint whose image the stack
// adds packages to runs on Debian's slim image, as a Go one does, with
// OpenSSL, which its binary links, among what its final stage installs.
func TestRustImageAdditions(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadRustFixture(t)
	stock := ir.ServiceRef{Name: "rs-stock", Kind: ir.SchemaKindAPI}
	pricing := ir.ServiceRef{Name: "rs-pricing", Kind: ir.SchemaKindAPI}
	f.withImages(t, rustStack,
		&ir.DeployableImage{Of: ir.DeployableRef{Service: &stock}, Packages: []string{"poppler-utils"}, Extend: "docker/stock.Dockerfile"},
		&ir.DeployableImage{Of: ir.DeployableRef{Service: &pricing}, Extend: "Dockerfile.pricing"},
	)
	writeExtension(t, repoRoot, "docker/stock.Dockerfile")
	if err := os.WriteFile(filepath.Join(repoRoot, "Dockerfile.pricing"), []byte("ARG BASE\nFROM ${BASE}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.build(t, repoRoot, rustFakePaths(repoRoot), rustOrder[:len(rustOrder)-1]...)
	compareGoldens(t, imageGoldenRoot, dockerFiles(repoRoot, "rust", rustStack, "rs-stock", "rs-pricing"))

	dockerfile, ignore := readDocker(t, repoRoot, rustStack, "rs-stock")
	wantAll(t, "rs-stock's Dockerfile", dockerfile,
		"\nFROM debian:trixie-slim\nRUN apt-get update \\\n    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \\\n"+
			"      ca-certificates \\\n      openssl \\\n      poppler-utils \\\n      tzdata \\\n    && rm -rf /var/lib/apt/lists/* \\\n",
		"COPY --from=build /out/rs-stock /rs-stock\nUSER nonroot\nWORKDIR /home/nonroot\n",
		"#   docker build -f docker/stock.Dockerfile --build-arg BASE=<the image this file built> .\n")
	wantAll(t, "rs-stock's ignore file", ignore, "\n!docker\n", "\n!docker/stock.Dockerfile\n")

	// A Dockerfile at the context's root brings itself alone: its
	// directory is the whole repository, which the ignore file cuts down.
	dockerfile, ignore = readDocker(t, repoRoot, rustStack, "rs-pricing")
	wantAll(t, "rs-pricing's Dockerfile", dockerfile, "\nFROM gcr.io/distroless/cc-debian13:nonroot\n", "#   docker build -f Dockerfile.pricing --build-arg BASE=")
	wantAll(t, "rs-pricing's ignore file", ignore, "\n!Dockerfile.pricing\n")
	if strings.Contains(ignore, "\n!.\n") {
		t.Errorf("rs-pricing's ignore file takes in the whole context:\n%s", ignore)
	}
}

// TestTheDockerfileWithImageAdditionsBuildsAnImageThatServes (D144):
// Storefront's Dockerfile, in cloudStack, with a package the stack adds
// and a Dockerfile of the project's that extends it, builds, and both
// images serve as the image with neither does. The generated image runs
// on Debian's slim image as the non-root user distroless ran as, with the
// package installed, CA certificates and no package lists. The extending
// Dockerfile builds over it from the context a deploy writes
// (stackdeploy.WriteContext), which holds it and the font beside it, with
// the build argument BASE naming the generated image, and keeps its user
// and its entrypoint. It needs Docker and the network, and runs only
// outside -short.
func TestTheDockerfileWithImageAdditionsBuildsAnImageThatServes(t *testing.T) {
	needDocker(t)
	repoRoot := dockerRepo(t)
	f := loadFixture(t, servicesRoot)
	f.withImages(t, cloudStack, &ir.DeployableImage{
		Of: ir.DeployableRef{Deployable: "Storefront"}, Packages: []string{"tini"}, Extend: "docker/storefront/Dockerfile",
	})
	writeExtension(t, repoRoot, "docker/storefront/Dockerfile")
	f.build(t, repoRoot, fakePaths(repoRoot), append(slices.Clone(apis), cloudStack)...)

	// The context a deploy writes holds the extending Dockerfile and what
	// lies beside it, and both builds run from it.
	dir := servergen.ServerDir(filepath.Join(repoRoot, "schemas", "dist"), cloudStack, "Storefront")
	archive := filepath.Join(t.TempDir(), "context.tar.gz")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	built, err := stackdeploy.WriteContext(file, repoRoot, filepath.Join(dir, servergen.DockerFile), filepath.Join(repoRoot, "docker", "storefront", "Dockerfile"))
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	if built.Extend != "docker/storefront/Dockerfile" {
		t.Fatalf("the context names its extending Dockerfile %q", built.Extend)
	}
	context := t.TempDir()
	if out, err := exec.Command("tar", "-xzf", archive, "-C", context).CombinedOutput(); err != nil {
		t.Fatalf("extract the context: %v\n%s", err, out)
	}

	stamp := time.Now().UnixNano()
	image := fmt.Sprintf("superschematic-servergen-test:%d-packages", stamp)
	dockerBuild(t, context, image, "-f", built.Dockerfile)
	sh := func(image, script string) string {
		t.Helper()
		return docker(t, "run", "--rm", "--entrypoint", "sh", image, "-c", script)
	}
	for script, want := range map[string]string{
		"id -u":           "65532",
		"id -un":          "nonroot",
		"command -v tini": "/usr/bin/tini",
		"test -s /etc/ssl/certs/ca-certificates.crt && echo held": "held",
		"test -d /usr/share/zoneinfo/Europe && echo held":         "held",
		"ls /var/lib/apt/lists | wc -l":                           "0",
		"touch \"$HOME/probe\" && echo written":                   "written",
		// It starts in the user's home, as distroless's nonroot image
		// does, which it may write.
		"pwd":                         "/home/nonroot",
		"touch probe && echo written": "written",
	} {
		if got := strings.TrimSpace(sh(image, script)); got != want {
			t.Errorf("in the image, `%s` prints %q, want %q", script, got, want)
		}
	}
	expectServes(t, image)

	extended := fmt.Sprintf("superschematic-servergen-test:%d-extended", stamp)
	dockerBuild(t, context, extended, "-f", built.Extend, "--build-arg", "BASE="+image)
	if got := sh(extended, "id -u && cat /usr/share/fonts/invoice.ttf"); got != "65532\na font" {
		t.Errorf("the extended image runs as, and holds, %q; want user 65532 and the font the extending Dockerfile copies", got)
	}
	expectServes(t, extended)
}
