package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/cli"
	"github.com/parable-work/superschematic/ir"
)

// buildRuntimeProviders copies examples/runtime-providers' schemas and
// provider packages into a project, lets edit change the stack's schema,
// and builds the stack with the installed binary, whose core registers
// notifications (D153) as a feature that needs a channel adapter.
// It returns the output root and the build's error.
func buildRuntimeProviders(t *testing.T, edit func(stack string) string) (string, error) {
	t.Helper()
	project, schemasRoot := runtimeProvidersProject(t, edit)
	t.Chdir(project)
	outputRoot := t.TempDir()
	buf := new(bytes.Buffer)
	root := cli.New(cli.Config{Name: "superschematic"}, extensions()...)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetIn(strings.NewReader(""))
	root.SetArgs([]string{"build", filepath.Join(schemasRoot, "services", "notes-stack"), "--out", outputRoot})
	if err := root.Execute(); err != nil {
		return outputRoot, err
	}
	return outputRoot, nil
}

// runtimeProvidersProject copies examples/runtime-providers' schemas and
// provider packages into a project, its paths pointing back at this
// checkout, and lets edit change the stack's schema. It returns the
// project and its schemas root.
func runtimeProvidersProject(t *testing.T, edit func(stack string) string) (string, string) {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	example := filepath.Join(repoRoot, "examples", "runtime-providers")
	project := t.TempDir()
	schemasRoot := filepath.Join(project, "schemas")
	copyTree(t, filepath.Join(example, "schemas", "services"), filepath.Join(schemasRoot, "services"))
	copyTree(t, filepath.Join(example, "providers"), filepath.Join(project, "providers"))
	rel, err := filepath.Rel(project, repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	rel = filepath.ToSlash(rel)
	for name, replace := range map[string]*strings.Replacer{
		"superschematic.toml": strings.NewReplacer(`"../../`, `"`+rel+"/", `"../.."`, `"`+rel+`"`),
		"tsconfig.base.json":  strings.NewReplacer(`"../../../`, `"../`+rel+"/"),
	} {
		data, err := os.ReadFile(filepath.Join(example, "schemas", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(schemasRoot, name), []byte(replace.Replace(string(data))), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if edit != nil {
		path := filepath.Join(schemasRoot, "services", "notes-stack", "src", "stack.schema.ts")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(edit(string(data))), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return project, schemasRoot
}

// TestAProviderSourceEditRebuildsItsAPI (#608): build-all's cache key of an
// API covers the sources of the provider packages its config lists. With
// examples/runtime-providers built once, a second run builds nothing; an
// edit to a source file of sms-relay-a, which notes-api lists by its
// directory outside the schemas, rebuilds notes-api and the stack that
// deploys it, and leaves notes-db, which lists none, up to date.
func TestAProviderSourceEditRebuildsItsAPI(t *testing.T) {
	project, schemasRoot := runtimeProvidersProject(t, nil)
	t.Chdir(project)
	outputRoot := t.TempDir()
	cacheRoot := t.TempDir()
	run := func() string {
		t.Helper()
		buf := new(bytes.Buffer)
		root := cli.New(cli.Config{Name: "superschematic"}, extensions()...)
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetIn(strings.NewReader(""))
		root.SetArgs([]string{"build-all", filepath.Join(schemasRoot, "services"), "--out", outputRoot, "--cache", "--cache-root", cacheRoot})
		if err := root.Execute(); err != nil {
			t.Fatalf("build-all: %v\n%s", err, buf)
		}
		return buf.String()
	}
	services := []string{"notes-db", "notes-api", "notes-stack"}
	first := run()
	for _, name := range services {
		if !strings.Contains(first, "OK: "+name+" (built") {
			t.Fatalf("the first run did not build %s:\n%s", name, first)
		}
	}
	second := run()
	for _, name := range services {
		if !strings.Contains(second, "OK: "+name+" (up to date") {
			t.Fatalf("the second run did not find %s up to date:\n%s", name, second)
		}
	}
	source := filepath.Join(project, "providers", "sms-relay-a", "src", "index.ts")
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, append(data, []byte("\n// edited\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	third := run()
	for name, want := range map[string]string{"notes-api": "built", "notes-stack": "built", "notes-db": "up to date"} {
		if !strings.Contains(third, "OK: "+name+" ("+want) {
			t.Errorf("after an edit to sms-relay-a's source, %s is not %s:\n%s", name, want, third)
		}
	}
}

// TestRuntimeProvidersExampleStack builds examples/runtime-providers'
// stack (D159): the build reads the six provider packages notes-api lists
// and checks each against its interface's contract, and each environment's
// environment.json shows the provider every slot names. Dev sends SMS
// through sms-relay-a and Cloudflare through sms-relay-b, a change of env
// alone.
func TestRuntimeProvidersExampleStack(t *testing.T) {
	out, err := buildRuntimeProviders(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Dev":        "MODEL=stub-model PREVIEWS=text-preview RUNNER=local-sandbox SMS=sms-relay-a VAULT=vault-broker",
		"Cloudflare": "PREVIEWS=text-preview SMS=sms-relay-b",
	}
	for env, slots := range want {
		data, err := os.ReadFile(filepath.Join(out, "stack", "notes-stack", env, "environment.json"))
		if err != nil {
			t.Fatal(err)
		}
		var resolved ir.ResolvedEnvironment
		if err := json.Unmarshal(data, &resolved); err != nil {
			t.Fatal(err)
		}
		api := resolved.Deployable("notes-api")
		if api == nil {
			t.Fatalf("%s has no notes-api", env)
		}
		var got []string
		for _, p := range api.Providers {
			if p.Service != "notes-api" || !strings.HasPrefix(p.Package, "@example-providers/") {
				t.Errorf("%s: provider %+v", env, p)
			}
			got = append(got, p.Slot+"="+p.Provider)
		}
		if strings.Join(got, " ") != slots {
			t.Errorf("%s's notes-api uses %v, want %s", env, got, slots)
		}
	}
}

// TestRuntimeProvidersExampleRefusals: an environment that runs notes-api,
// whose database raises events for the notifier (D153), with no channel
// adapter is refused, as is
// one that swaps in sms-relay-b and leaves its account unbound, and one
// whose slot names a provider of another interface.
func TestRuntimeProvidersExampleRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(string) string
		want string
	}{
		"no channel": {
			edit: func(s string) string {
				return strings.Replace(s, `        SMS: "sms-relay-b",
        SMS_URL: "https://sms-relay-b.example.test",
        SMS_ACCOUNT: "AC100",
`, "", 1)
			},
			want: "environment Cloudflare runs server notes-api, whose API notes-api uses notifications, which needs a channel adapter, and the environment configures none: set SMS to one of sms-relay-a, sms-relay-b in its env (D159)",
		},
		"a setting left unbound": {
			edit: func(s string) string { return strings.Replace(s, `        SMS_ACCOUNT: "AC100",`+"\n", "", 1) },
			want: "provider slot SMS of notes-api names provider sms-relay-b, which needs setting ACCOUNT, and the environment binds no value to SMS_ACCOUNT",
		},
		"another interface": {
			edit: func(s string) string { return strings.Replace(s, `SMS: "sms-relay-b"`, `SMS: "vault-broker"`, 1) },
			want: "provider slot SMS of notes-api takes a channel adapter, and provider vault-broker (@example-providers/vault-broker) implements credentials",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := buildRuntimeProviders(t, tc.edit)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the build answered %v, want a refusal that says %q", err, tc.want)
			}
		})
	}
}
