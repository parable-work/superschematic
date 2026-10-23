package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/loader/tsreader"
	"github.com/parable-work/superschematic/internal/loader/verify"
)

// runCheckCommand runs `check` with args and returns its stdout and error.
func runCheckCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	root := New(Config{})
	root.SetOut(&out)
	root.SetErr(new(bytes.Buffer))
	root.SetArgs(append([]string{"check"}, args...))
	err := root.Execute()
	return out.String(), err
}

// checkJSON runs `check --json` with args and decodes its document.
func checkJSON(t *testing.T, args ...string) (checkReport, error) {
	t.Helper()
	out, err := runCheckCommand(t, append([]string{"--json"}, args...)...)
	var report checkReport
	require.NoError(t, json.Unmarshal([]byte(out), &report), out)
	return report, err
}

func serviceStatuses(report checkReport) map[string]string {
	statuses := map[string]string{}
	for _, service := range report.Services {
		statuses[service.Name] = service.Status
	}
	return statuses
}

// TestCheckPassesACleanTreeAndWritesNoOutputs: a check loads every service
// under a services root, reports each ok, and writes nothing under the
// output root.
func TestCheckPassesACleanTreeAndWritesNoOutputs(t *testing.T) {
	servicesRoot := prepareTSServicesRoot(t, "fixture-chain-base", "fixture-chain-common", "fixture-chain-db", "fixture-chain-api")

	out, err := runCheckCommand(t, servicesRoot)
	require.NoError(t, err, out)
	assert.Regexp(t, `^Checked 4 schema services in \d+ms: ok\n$`, out)
	assert.NoDirExists(t, filepath.Join(filepath.Dir(servicesRoot), "dist"))
	assert.FileExists(t, filepath.Join(servicesRoot, "fixture-chain-api", "src", "service.generated.ts"), "a check writes the sentinels a build writes")

	// A schemas root names its services root.
	out, err = runCheckCommand(t, filepath.Dir(servicesRoot))
	require.NoError(t, err, out)
	assert.Contains(t, out, "Checked 4 schema services")
}

// TestCheckReportsEachErrorWithItsLocation: --json lists every error with
// its service, file, line, column, severity and message, marks each
// failing service, and the command exits 1.
func TestCheckReportsEachErrorWithItsLocation(t *testing.T) {
	servicesRoot := prepareTSServicesRoot(t, "fixture-db", "broken-type-error", "broken-illegal-import")

	report, err := checkJSON(t, servicesRoot)
	var exit *ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, 1, exit.Code)

	assert.False(t, report.OK)
	assert.Equal(t, 2, report.Errors)
	assert.ElementsMatch(t, []string{"fixture-db", "broken-type-error", "broken-illegal-import"}, report.Checked)
	assert.Equal(t, map[string]string{"fixture-db": "ok", "broken-type-error": "error", "broken-illegal-import": "error"}, serviceStatuses(report))

	byService := map[string]checkDiagnostic{}
	for _, d := range report.Diagnostics {
		byService[d.Service] = d
	}
	illegal := byService["broken-illegal-import"]
	assert.Equal(t, checkDiagnostic{
		Service:  "broken-illegal-import",
		File:     filepath.Join(servicesRoot, "broken-illegal-import", "src", "illegal.schema.ts"),
		Line:     2,
		Column:   1,
		Severity: "error",
		Message:  "a General schema cannot import @superschematic/db",
	}, illegal)
	typeError := byService["broken-type-error"]
	assert.Equal(t, filepath.Join(servicesRoot, "broken-type-error", "src", "broken.schema.ts"), typeError.File)
	assert.Equal(t, 4, typeError.Line)
	assert.Contains(t, typeError.Message, "has no exported member 'DoesNotExist'")

	// The text form writes each as a compiler does, then a summary.
	out, err := runCheckCommand(t, servicesRoot)
	require.Error(t, err)
	assert.Contains(t, out, filepath.Join(servicesRoot, "broken-illegal-import", "src", "illegal.schema.ts")+":2:1: error: a General schema cannot import @superschematic/db\n")
	assert.Contains(t, out, ": 2 errors; failed: ")
}

// TestCheckOfAServiceChecksItsDependencies: given a service directory,
// check loads the service and every service it depends on, and no other.
func TestCheckOfAServiceChecksItsDependencies(t *testing.T) {
	servicesRoot := prepareTSServicesRoot(t, "fixture-chain-base", "fixture-chain-common", "fixture-chain-db", "fixture-chain-api", "broken-type-error")

	report, err := checkJSON(t, filepath.Join(servicesRoot, "fixture-chain-db"))
	require.NoError(t, err)
	assert.True(t, report.OK)
	assert.Equal(t, []string{"fixture-chain-base", "fixture-chain-common", "fixture-chain-db"}, report.Checked)
	assert.Len(t, report.Services, 3)
}

// TestCheckBlocksAServiceOnTheDependencyItReads: a service whose load reads
// a dependency that fails is blocked by it, and the dependency's errors
// are reported once, as the dependency's, located in its data-form file.
func TestCheckBlocksAServiceOnTheDependencyItReads(t *testing.T) {
	servicesRoot := prepareTSServicesRoot(t, "fixture-user-model-db", "fixture-user-routes-api")
	users := filepath.Join(servicesRoot, "fixture-user-model-db", "src", "users.schema.json")
	require.NoError(t, os.WriteFile(users, []byte(`{"types": `), 0o644))

	report, err := checkJSON(t, servicesRoot)
	require.Error(t, err)
	assert.Equal(t, map[string]string{"fixture-user-model-db": "error", "fixture-user-routes-api": "blocked"}, serviceStatuses(report))
	for _, service := range report.Services {
		if service.Name == "fixture-user-routes-api" {
			assert.Equal(t, []string{"fixture-user-model-db"}, service.BlockedBy)
		}
	}
	require.NotEmpty(t, report.Diagnostics)
	for _, d := range report.Diagnostics {
		assert.Equal(t, "fixture-user-model-db", d.Service, d.Message)
		assert.Equal(t, users, d.File, d.Message)
	}

	out, _ := runCheckCommand(t, servicesRoot)
	assert.Contains(t, out, "blocked: fixture-user-routes-api (by fixture-user-model-db)")
}

// TestCheckReportsWarningsWithoutFailing: a verification warning is a
// diagnostic of its own severity, located in its file, and leaves the
// check ok.
func TestCheckReportsWarningsWithoutFailing(t *testing.T) {
	servicesRoot := filepath.Join(t.TempDir(), "schemas", "services")
	service := filepath.Join(servicesRoot, "temp-api")
	require.NoError(t, os.MkdirAll(filepath.Join(service, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(service, "schema.config.json"), []byte(`{"name": "temp-api", "kind": "API", "outputs": {}}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(service, "src", "a.schema.json"), []byte(`{
		"types": {
			"Tenant": {
				"name": "Tenant",
				"role": "EmbeddedStruct",
				"fields": [
					{"name": "id", "typeRef": {"name": "string"}, "required": true},
					{"name": "name", "typeRef": {"name": "string"}, "required": true, "sourceMustProject": true}
				]
			},
			"TenantView": {
				"name": "TenantView",
				"role": "APIView",
				"source": {"target": "temp-api.Tenant"},
				"fields": [{"name": "id", "typeRef": {"name": "string"}, "required": true}]
			}
		}
	}`), 0o644))

	report, err := checkJSON(t, servicesRoot)
	require.NoError(t, err)
	assert.True(t, report.OK)
	assert.Equal(t, 1, report.Warnings)
	require.Len(t, report.Diagnostics, 1)
	assert.Equal(t, "warning", report.Diagnostics[0].Severity)
	assert.Equal(t, filepath.Join(service, "src", "a.schema.json"), report.Diagnostics[0].File)
	assert.Contains(t, report.Diagnostics[0].Message, "TenantView omits temp-api.Tenant.name")
}

// TestCheckResolvesWhatItChecks: a service directory, a services root, a
// schemas root, and with no argument the working directory or
// ./schemas/services.
func TestCheckResolvesWhatItChecks(t *testing.T) {
	project := t.TempDir()
	services := filepath.Join(project, "schemas", "services")
	service := filepath.Join(services, "shop-db")
	require.NoError(t, os.MkdirAll(service, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(service, "schema.config.json"), []byte(`{}`), 0o644))

	for _, tc := range []struct {
		cwd, path, root, service string
	}{
		{project, service, services, service},
		{project, services, services, ""},
		{project, filepath.Join(project, "schemas"), services, ""},
		{project, "", services, ""},
		{filepath.Join(project, "schemas"), "", services, ""},
		{service, "", services, service},
	} {
		t.Run(fmt.Sprintf("%s in %s", tc.path, tc.cwd), func(t *testing.T) {
			t.Chdir(tc.cwd)
			root, target, err := resolveCheckTarget(tc.path)
			require.NoError(t, err)
			assert.Equal(t, evalSymlinks(t, tc.root), evalSymlinks(t, root))
			if tc.service == "" {
				assert.Empty(t, target)
			} else {
				assert.Equal(t, evalSymlinks(t, tc.service), evalSymlinks(t, target))
			}
		})
	}
	_, _, err := resolveCheckTarget(filepath.Join(project, "missing"))
	assert.ErrorContains(t, err, "directory not found")
}

func evalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	return resolved
}

// TestDiagnosticsOfKeepsEachLocation: a load's error tree splits into its
// findings, each located where the loader or a data-form reader put it,
// and a failed dependency becomes a blocking edge, not a finding.
func TestDiagnosticsOfKeepsEachLocation(t *testing.T) {
	dir := filepath.FromSlash("/schemas/services/shop-db")
	blocked := map[string]bool{}
	err := errors.Join(
		fmt.Errorf("reading config: %w", tsreader.SchemaErrorList{{File: "/abs/schema.config.ts", Line: 3, Col: 16, Msg: "bad config"}}),
		&verify.Diagnostic{Severity: verify.SeverityError, File: "src/a.schema.ts", Msg: "file-level"},
		errors.New("src/b.schema.yaml: line 2: mapping values are not allowed\nin this context"),
		errors.New("src/c.schema.json:4:2: positioned"),
		fmt.Errorf("shop-api: reading the authDb shop-db: %w", &dependencyFailedError{name: "shop-db"}),
		errors.New("shop-db: type Order: unknown type Money"),
	)

	got := diagnosticsOf(err, dir, blocked)
	assert.Equal(t, []checkDiagnostic{
		{File: "/abs/schema.config.ts", Line: 3, Column: 16, Severity: "error", Message: "bad config"},
		{File: filepath.Join(dir, "src", "a.schema.ts"), Severity: "error", Message: "file-level"},
		{File: filepath.Join(dir, "src", "b.schema.yaml"), Severity: "error", Message: "line 2: mapping values are not allowed\nin this context"},
		{File: filepath.Join(dir, "src", "c.schema.json"), Line: 4, Column: 2, Severity: "error", Message: "positioned"},
		{Severity: "error", Message: "shop-db: type Order: unknown type Money"},
	}, got)
	assert.Equal(t, map[string]bool{"shop-db": true}, blocked)
}

// TestCheckWatchRechecksWhatAnEditAffects: --watch checks every service,
// then on each edit re-checks the edited service and its dependents only,
// writing one JSON document per line, and stops when its context ends.
func TestCheckWatchRechecksWhatAnEditAffects(t *testing.T) {
	servicesRoot := prepareTSServicesRoot(t, "fixture-chain-base", "fixture-chain-common", "fixture-chain-db", "fixture-chain-api", "fixture-db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &lockedBuffer{}
	root := New(Config{})
	root.SetOut(out)
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"check", "--watch", "--json", "--interval", "20ms", servicesRoot})
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	next := watchReports(t, out)
	first := next()
	assert.True(t, first.OK)
	assert.Len(t, first.Checked, 5)

	// An edit to fixture-chain-common re-checks it and the services that
	// depend on it, not fixture-chain-base or fixture-db.
	schemaFile := firstSchemaFile(t, filepath.Join(servicesRoot, "fixture-chain-common"))
	original, err := os.ReadFile(schemaFile)
	require.NoError(t, err)
	writeByRename(t, schemaFile, append(append([]byte(nil), original...), "\nexport abstract class Broken { value: NoSuchType; }\n"...))
	broken := next()
	assert.False(t, broken.OK)
	assert.Equal(t, []string{"fixture-chain-common", "fixture-chain-db", "fixture-chain-api"}, broken.Checked)
	assert.Equal(t, []string{schemaFile}, broken.Changed)
	assert.Equal(t, "error", serviceStatuses(broken)["fixture-chain-common"])
	assert.Equal(t, "ok", serviceStatuses(broken)["fixture-db"], "a service the edit cannot change keeps its last result")
	require.NotEmpty(t, broken.Diagnostics)
	assert.Contains(t, broken.Diagnostics[0].Message, "NoSuchType")

	writeByRename(t, schemaFile, original)
	fixed := next()
	assert.True(t, fixed.OK, "%+v", fixed.Diagnostics)

	// An edit to a service nothing depends on re-checks that service alone.
	dbFile := firstSchemaFile(t, filepath.Join(servicesRoot, "fixture-db"))
	writeByRename(t, dbFile, append(mustRead(t, dbFile), "\n"...))
	assert.Equal(t, []string{"fixture-db"}, next().Checked)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "an interrupted watch ends cleanly")
	case <-time.After(10 * time.Second):
		t.Fatal("check --watch did not stop when its context ended")
	}
}

// watchReports returns a function that waits for the next document a
// watch writes to out and decodes it.
func watchReports(t *testing.T, out *lockedBuffer) func() checkReport {
	read := 0
	return func() checkReport {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			scanner := bufio.NewScanner(strings.NewReader(out.String()))
			scanner.Buffer(nil, 1<<20)
			var lines []string
			for scanner.Scan() {
				lines = append(lines, scanner.Text())
			}
			if len(lines) > read {
				var report checkReport
				require.NoError(t, json.Unmarshal([]byte(lines[read]), &report), lines[read])
				read++
				return report
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("no watch result after %d; output:\n%s", read, out.String())
		return checkReport{}
	}
}

// firstSchemaFile returns the first *.schema.ts file of a service.
func firstSchemaFile(t *testing.T, service string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(service, "src", "*.schema.ts"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	return files[0]
}

// writeByRename replaces path's content as an editor saves it, so a poll
// never sees the file half written.
func writeByRename(t *testing.T, path string, data []byte) {
	t.Helper()
	tmp := filepath.Join(filepath.Dir(path), ".edit.tmp")
	require.NoError(t, os.WriteFile(tmp, data, 0o644))
	require.NoError(t, os.Rename(tmp, path))
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
