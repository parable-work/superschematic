package servergen_test

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/servergen"
)

// TestRustEngineEntrypointGolden: desk-rs-api's Rust server opens the
// engine of desk-db in the background (engine.rs), deploys its engine
// document before /readyz reports it ready, mounts its routes beside
// desk-rs-api's at /engine/desk-db behind the identity runtime's session
// and the service authenticator, and holds it in desk-rs-api's Deps;
// desk-rs-api's job opens it for its Deps and deploys nothing; desk-db's
// runner, in Rust, opens the engine and runs its runner as itself, holding
// each engine type (D58). Each crate depends on the engine's crate and the
// core's behaviors by path, and each image's context holds the engine's
// workspace. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestRustEngineEntrypointGolden -update
func TestRustEngineEntrypointGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadEngineFixture(t)
	f.build(t, repoRoot, rustFakePaths(repoRoot), rustEngineOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	files := map[string]string{}
	for _, d := range []struct {
		name  string
		files []string
	}{
		{rustEngineServer, []string{servergen.RustManifestFile, servergen.RustMainFile, servergen.RustEngineFile, servergen.DockerFile, servergen.DockerIgnoreFile}},
		{engineRunner, []string{servergen.RustManifestFile, servergen.RustMainFile, servergen.RustEngineFile, servergen.DockerFile}},
		{rustEngineJob, []string{servergen.RustManifestFile, servergen.RustMainFile, servergen.RustEngineFile}},
	} {
		for _, file := range d.files {
			files[filepath.Join("server", rustEngineStack, d.name, filepath.FromSlash(file))] = filepath.Join(servergen.ServerDir(out, rustEngineStack, d.name), filepath.FromSlash(file))
		}
	}
	for _, file := range []string{"Cargo.toml", "src/deps.rs"} {
		files[filepath.Join("api", "desk-rs-api", filepath.FromSlash(file))] = filepath.Join(generator.APIDir(out, "desk-rs-api"), filepath.FromSlash(file))
	}
	compareGoldens(t, filepath.Join(goldenRoot, "engine"), files)
}
