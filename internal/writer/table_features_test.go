package writer

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/jsonreader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// featureServices are the fixtures and examples of the table features: the
// calendar and Recurrence (D152), events, the notifier and channel
// identities (D153), references and attachments (D154), search (D155),
// per-record access, grants and the access log (D156), parties and
// conversations (D157), the agent orchestration and autonomy (D158), and
// provider slots (D159), with the Aliased names a merge keeps (D147),
// which the access fixtures use. Each is a directory holding
// schema.config.ts, beside the services it depends on.
var featureServices = []string{
	tsFixtures + "/fixture-access-api",
	tsFixtures + "/fixture-access-db",
	tsFixtures + "/fixture-access-go-api",
	tsFixtures + "/fixture-access-go-db",
	tsFixtures + "/fixture-agent-runs-db",
	tsFixtures + "/fixture-agent-runs-go-api",
	tsFixtures + "/fixture-aliased-db",
	tsFixtures + "/fixture-audience-queue-api",
	tsFixtures + "/fixture-audience-queue-db",
	tsFixtures + "/fixture-audience-behaviors-api",
	tsFixtures + "/fixture-audience-behaviors-db",
	tsFixtures + "/fixture-calendar-db",
	tsFixtures + "/fixture-merge-access-db",
	tsFixtures + "/fixture-notifications-db",
	tsFixtures + "/fixture-parties-db",
	tsFixtures + "/fixture-providers-api",
	tsFixtures + "/fixture-providers-db",
	tsFixtures + "/fixture-providers-go-api",
	tsFixtures + "/fixture-recurrence-api",
	tsFixtures + "/fixture-recurrence-db",
	tsFixtures + "/fixture-references-db",
	tsFixtures + "/fixture-search-db",
	tsFixtures + "/fixture-worker-refs-api",
	tsFixtures + "/fixture-worker-refs-db",
	"../../examples/access-records/schemas/services/records-api",
	"../../examples/access-records/schemas/services/records-db",
	"../../examples/agent-orchestration/schemas/services/work-api",
	"../../examples/agent-orchestration/schemas/services/work-db",
	"../../examples/deploy-smoke/schemas/services/smoke-db",
	"../../examples/notifications/schemas/services/tickets-api",
	"../../examples/notifications/schemas/services/tickets-db",
	"../../examples/parties/schemas/services/jobs-api",
	"../../examples/parties/schemas/services/jobs-db",
	"../../examples/recurring-duties/schemas/services/duties-db",
	"../../examples/references-files/schemas/services/issues-db",
	"../../examples/runtime-providers/schemas/services/notes-api",
	"../../examples/runtime-providers/schemas/services/notes-db",
	"../../examples/search-across-tables/schemas/services/workspace-db",
}

// updateWritten rewrites the written TypeScript's goldens under
// testdata/written-ts.
var updateWritten = flag.Bool("update", false, "rewrite testdata/written-ts")

// TestTSWriterRoundTripsTableFeatures is the round trip `format --to=ts`
// stands on for the table features' decorators: every fixture and example
// service, read, written back to TypeScript in place of its schema files,
// and read again, gives the same IR, and so does the TypeScript written
// from the service's JSON form, which a data-form file converts through.
// The TypeScript written is held to testdata/written-ts (-update rewrites
// it).
func TestTSWriterRoundTripsTableFeatures(t *testing.T) {
	for _, service := range featureServices {
		t.Run(filepath.Base(service), func(t *testing.T) {
			// The writer names service packages through the active naming,
			// which format discovers from the file it converts.
			names, err := naming.Discover(service)
			if err != nil {
				t.Fatal(err)
			}
			previous := naming.Active()
			naming.SetActive(names)
			t.Cleanup(func() { naming.SetActive(previous) })
			schema, err := loader.LoadService(service, loader.WithNaming(names))
			if err != nil {
				t.Fatalf("loading %s: %v", service, err)
			}
			want := normalizeIR(t, schema)

			reloaded, written := rewriteInPlace(t, service, schema, names, FormatTS)
			if got := normalizeIR(t, reloaded); got != want {
				t.Errorf("IR mismatch after TS -> TS round trip of %s\n%s", service, firstDiff(want, got))
			}
			checkWrittenGoldens(t, filepath.Join("testdata", "written-ts", filepath.Base(service)), written)

			fromJSON, _ := rewriteInPlace(t, service, schema, names, FormatJSON)
			reloaded, _ = rewriteInPlace(t, service, fromJSON, names, FormatTS)
			if got := normalizeIR(t, reloaded); got != want {
				t.Errorf("IR mismatch after TS -> JSON -> TS round trip of %s\n%s", service, firstDiff(want, got))
			}
		})
	}
}

// rewriteInPlace copies the service at dir, with its config and project
// files, into a mirror of the repository, writes schema in format in place
// of its schema files and loads the copy. It returns the schema the copy
// loads and the copy's directory. The schema carries its @source lineage
// imports, as a data-form file does, so written TypeScript imports the
// classes it projects.
func rewriteInPlace(t *testing.T, dir string, schema *ir.Schema, names naming.Naming, format Format) (*ir.Schema, string) {
	t.Helper()
	copyDir := stageService(t, dir)
	if _, err := WriteService(withSourceLineageImports(schema), format, copyDir); err != nil {
		t.Fatalf("WriteService(%s): %v", format, err)
	}
	reloaded, err := loader.LoadService(copyDir, loader.WithNaming(names))
	if err != nil {
		dumpService(t, filepath.Join(copyDir, "src"))
		t.Fatalf("reloading the written %s: %v", format, err)
	}
	return reloaded, copyDir
}

// checkWrittenGoldens holds each TypeScript schema file written under dir
// to its golden under goldens, <path>.golden, and holds goldens to no file
// the writer did not write.
func checkWrittenGoldens(t *testing.T, goldens, dir string) {
	t.Helper()
	written := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".schema.ts") {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		data, err := os.ReadFile(path)
		written[filepath.ToSlash(rel)+".golden"] = data
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if *updateWritten {
		if err := os.RemoveAll(goldens); err != nil {
			t.Fatal(err)
		}
		for rel, data := range written {
			path := filepath.Join(goldens, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	held := map[string]bool{}
	_ = filepath.WalkDir(goldens, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(goldens, path)
			held[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	for rel, data := range written {
		want, err := os.ReadFile(filepath.Join(goldens, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("%s/%s: %v; run with -update", goldens, rel, err)
			continue
		}
		if string(want) != string(data) {
			t.Errorf("%s/%s is stale; run with -update\n%s", goldens, rel, firstDiff(string(want), string(data)))
		}
		delete(held, rel)
	}
	for rel := range held {
		t.Errorf("%s/%s holds no file the writer writes; run with -update", goldens, rel)
	}
}

// stageService lays out a temporary mirror of the repository holding a copy
// of the service at dir without its schema files: every other entry of each
// directory from dir's parent up to the repository's root is linked to the
// original, so the copy's config, tsconfig and sibling services resolve
// every relative path the original does. It returns the copy's path.
func stageService(t *testing.T, dir string) string {
	t.Helper()
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(repo, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("%s is outside the repository", dir)
	}
	stage := t.TempDir()
	copyDir := filepath.Join(stage, rel)
	for child := abs; child != repo; child = filepath.Dir(child) {
		parent := filepath.Dir(child)
		entries, err := os.ReadDir(parent)
		if err != nil {
			t.Fatal(err)
		}
		mirrored := filepath.Join(stage, strings.TrimPrefix(parent, repo))
		if err := os.MkdirAll(mirrored, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.Name() == filepath.Base(child) {
				continue
			}
			if err := os.Symlink(filepath.Join(parent, entry.Name()), filepath.Join(mirrored, entry.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	err = filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(abs, path)
		target := filepath.Join(copyDir, rel)
		if d.IsDir() {
			if d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return os.MkdirAll(target, 0o755)
		}
		for _, ext := range schemaFileExtensions {
			if strings.HasSuffix(path, ext) {
				return nil
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return copyDir
}

// firstDiff reports the first line where want and got differ, with a few
// lines of context.
func firstDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		if i < len(w) && i < len(g) && w[i] == g[i] {
			continue
		}
		from := max(0, i-8)
		var b strings.Builder
		b.WriteString("want:\n")
		for j := from; j < min(len(w), i+8); j++ {
			b.WriteString("  " + w[j] + "\n")
		}
		b.WriteString("got:\n")
		for j := from; j < min(len(g), i+8); j++ {
			b.WriteString("  " + g[j] + "\n")
		}
		return b.String()
	}
	return ""
}

// TestWriteForWritesTheTableForm: WriteFor, which format --to=ts calls with
// the registry the file was read with, writes a General type that composes
// behaviors on the type as the table it publishes as, as WriteWith does
// (D117), while it also hands the writer the registry's declarations (D147).
func TestWriteForWritesTheTableForm(t *testing.T) {
	path := filepath.Join("..", "migrateplan", "testdata", "development", "notes.schema.json")
	doc, err := jsonreader.ReadFileWith(path, filepath.Base(path), nil)
	if err != nil {
		t.Fatal(err)
	}
	want, err := WriteWith(doc, FormatTS, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := WriteFor(doc, FormatTS, registry.New(naming.Active()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "@behavior") {
		t.Errorf("WriteFor writes @behavior, not the table form:\n%s", got)
	}
	if string(got) != string(want) {
		t.Errorf("WriteFor differs from WriteWith:\ngot:\n%s\nwant:\n%s", got, want)
	}
}
