package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// sqliteAccessGoCheck is the test the Go ORM of fixture-access-db runs on
// SQLite: a held database's repositories refuse every call there, naming
// why, and so do its References and its access log's repository.
const sqliteAccessGoCheck = `package orm

import (
	"context"
	"errors"
	"testing"
)

func TestTheRepositoriesOnSQLiteRefusePerRecordAccess(t *testing.T) {
	ctx := context.Background()
	db := &SQLiteDatabase{}
	if _, _, err := db.GetFolderRepository().FindMany(ctx, nil, nil); !errors.Is(err, ErrSQLiteUnsupported) {
		t.Errorf("Folder's FindMany on SQLite answered %v, want ErrSQLiteUnsupported", err)
	}
	if _, _, err := db.GetGrantRepository().FindMany(ctx, nil, nil); !errors.Is(err, ErrSQLiteUnsupported) {
		t.Errorf("Grant's FindMany on SQLite answered %v, want ErrSQLiteUnsupported", err)
	}
	if _, _, err := db.GetAccessEntryRepository().FindMany(ctx, nil, nil); !errors.Is(err, ErrSQLiteUnsupported) {
		t.Errorf("AccessEntry's FindMany on SQLite answered %v, want ErrSQLiteUnsupported", err)
	}
}
`

// sqliteAccessTSCheck is the bun test the TypeScript types package of
// fixture-access-db runs: its repositories on bun:sqlite, over the
// SQLite DDL the build wrote, keep a private folder to its owner, and a
// named one to the principals it names.
const sqliteAccessTSCheck = `import { Database } from 'bun:sqlite';
import { expect, test } from 'bun:test';
import { mkdtempSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { openSqlite } from '@superschematic/http-runtime/sqlite';
import { createOrm } from './orm-sqlite';

const alice = '1sK9QeT4oPWYz7Xs3ZcU6v';
const bob = '2bQ8HrS5nLMw3Jy6ZaT1kX';
const carol = '3cKSm7Pv2EM8Wl4z4XvMh1';

test('a private folder is its owner alone, and a named one is the principals it names, on bun:sqlite', async () => {
  const file = join(mkdtempSync(join(tmpdir(), 'access-db-')), 'access.db');
  const ddl = new Database(file, { create: true });
  ddl.exec(readFileSync(process.env.SQLITE_CREATE_SQL!, 'utf8'));
  ddl.close();
  const db = openSqlite(file);
  try {
    const asAlice = createOrm(db, alice);
    const mine = (await asAlice.folder.createOne({ name: 'Taxes', owner: alice, audience: 'private' } as never)) as { id: string };
    expect(((await asAlice.folder.getOne(mine.id as never)) as { name: string }).name).toBe('Taxes');
    expect(await createOrm(db, bob).folder.findOne({ id: { eq: mine.id } } as never)).toBeNull();
    const shared = (await asAlice.folder.createOne({ name: 'Trip', owner: alice, audience: 'named', audienceNamed: [bob] } as never)) as { id: string };
    expect(((await createOrm(db, bob).folder.getOne(shared.id as never)) as { name: string }).name).toBe('Trip');
    expect(await createOrm(db, carol).folder.findOne({ id: { eq: shared.id } } as never)).toBeNull();
  } finally {
    db.close();
  }
});
`

// TestBuildAnAccessDatabaseOnSQLite builds fixture-access-db, with sqlite
// listed first and Go's types on, through the build command (D182): a
// DB service that lists sqlite and declares @audience, @accessGrants and
// @accessLog builds, since the DB kind writes the Go ORM always. The Go
// ORM builds and vets, and its repositories on SQLite answer
// ErrSQLiteUnsupported for each call (D180); the TypeScript repositories
// on bun:sqlite keep each folder to its audience. It skips in -short mode
// and without bun, unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS is set.
func TestBuildAnAccessDatabaseOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: the test builds and runs the generated Go and TypeScript")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	testpaths.BuildHTTPRuntime(t, bun, paths)
	const name = "fixture-access-db"
	repoRoot := testpaths.RepoRoot(t)
	fixtures := filepath.Join(repoRoot, "internal", "loader", "tsreader", "testdata")

	repo := t.TempDir()
	schemasRoot := filepath.Join(repo, "schemas")
	serviceDir := filepath.Join(schemasRoot, "services", name)
	copyDir(t, filepath.Join(fixtures, "services", name), serviceDir)
	base := filepath.ToSlash(filepath.Join(fixtures, "tsconfig.base.json"))
	writeFiles(t, serviceDir, map[string]string{
		"tsconfig.json": `{"extends": "` + base + `", "include": ["schema.config.ts", "src/**/*.ts"], "exclude": ["src/**/*.test.ts"]}`,
		// sqlite first, Go's types beside TypeScript's, and the key of
		// Agreement's Encrypted<T> terms (D85), which fixture-access-api's
		// AccessKeys declares.
		"schema.config.ts": `import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "fixture-access-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    sql: { dialects: ["sqlite", "postgres"], encryption: { key: "AccessKeys.ACCESS_FIELD_KEYS" } }
  }
});
`,
	})
	// The generated modules take the runtimes from this checkout.
	var toml strings.Builder
	toml.WriteString("[paths]\n")
	for key, dir := range map[string]string{
		"scalar_go":               paths.ScalarGo,
		"scalar_typescript":       paths.ScalarTypeScript,
		"schema_ir":               paths.SchemaIR,
		"schema_runtime_go":       paths.SchemaRuntimeGo,
		"http_runtime_go":         paths.HTTPRuntimeGo,
		"http_runtime_typescript": paths.HTTPRuntimeTypeScript,
		"versiongraph_go":         paths.VersionGraphGo,
		"rules_go":                paths.RulesGo,
		"rules_typescript":        paths.RulesTypeScript,
	} {
		// [paths] are relative to the parent of the schemas root.
		rel, err := filepath.Rel(repo, dir)
		require.NoError(t, err)
		fmt.Fprintf(&toml, "%s = %q\n", key, filepath.ToSlash(rel))
	}
	writeFiles(t, schemasRoot, map[string]string{"superschematic.toml": toml.String()})

	outputRoot := t.TempDir()
	buf := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"build", serviceDir, "--out", outputRoot})
	require.NoError(t, root.Execute(), buf.String())

	// The Go ORM builds and vets, and its repositories refuse on SQLite.
	ormDir := generator.ORMDir(outputRoot, name)
	require.FileExists(t, filepath.Join(ormDir, "sqlite.go"), buf.String())
	writeFiles(t, ormDir, map[string]string{"sqlite_access_test.go": sqliteAccessGoCheck})
	var out []byte
	for _, args := range [][]string{{"mod", "tidy"}, {"vet", "./..."}, {"test", "-count=1", "-v", "-run", "TestTheRepositoriesOnSQLiteRefusePerRecordAccess", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = ormDir
		out, err = cmd.CombinedOutput()
		require.NoError(t, err, "go %s in the Go ORM: %s", strings.Join(args, " "), out)
	}
	require.Contains(t, string(out), "--- PASS: TestTheRepositoriesOnSQLiteRefusePerRecordAccess")

	// The TypeScript repositories run on bun:sqlite.
	typesDir := generator.TypesDir(outputRoot, "typescript", name)
	require.FileExists(t, filepath.Join(typesDir, "orm-sqlite.ts"), buf.String())
	install := exec.Command(bun, "install")
	install.Dir = typesDir
	out, err = install.CombinedOutput()
	require.NoError(t, err, "bun install: %s", out)
	writeFiles(t, typesDir, map[string]string{"access_sqlite.test.ts": sqliteAccessTSCheck})
	run := exec.Command(bun, "test", "./access_sqlite.test.ts")
	run.Dir = typesDir
	run.Env = append(os.Environ(), "SQLITE_CREATE_SQL="+filepath.Join(generator.SQLDir(outputRoot, name), "sqlite", "create.sql"))
	out, err = run.CombinedOutput()
	require.NoError(t, err, "bun test: %s", out)
	require.Contains(t, string(out), "1 pass", string(out))
}
