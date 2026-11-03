package servergen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/generator/tsrestgen"
	ir "github.com/parable-work/superschematic/ir"
)

// TestAnEntrypointOpensTheORMWithItsFieldKeys: a server, a job and a
// worker of an API whose database has Encrypted<T> fields open its ORM
// with the keys the API's config holds (D85): in Go, ConnectWithPool
// with WithFieldKeys of the EnvConfig field, and in TypeScript, createOrm
// with fieldKeys, the SecretValue the env config holds the secret in.
func TestAnEntrypointOpensTheORMWithItsFieldKeys(t *testing.T) {
	config := &envgen.ConfigOutput{
		SchemaName: "vault-api",
		EnvConfig:  true,
		Fields:     []envgen.ConfigField{{Key: "VAULT_DB_FIELD_KEYS", GoName: "VaultDbFieldKeys", Secret: true}},
		Derived:    []envgen.DerivedField{{Key: "VAULT_DB_DATABASE", GoName: "VaultDbDatabase", Kind: ir.EdgeSQL, Service: "vault-db"}},
	}

	goAPI := servergen.APIInput{
		Output: &apigen.APIOutput{
			SchemaName: "vault-api",
			ModulePath: "example.com/schemas/api/vault-api",
			EnvConfig:  config,
			Deps:       apigen.DepsInfo{Database: "vault-db", ORMModule: "example.com/schemas/orm/vault-db", FieldKeys: "VaultDbFieldKeys"},
			Jobs:       []apigen.JobInfo{{Name: "SyncItems", Method: "SyncItems"}},
		},
		Implementation: servergen.Implementation{Import: "example.com/schemas/implementation/vault-api"},
	}
	for _, tc := range []struct {
		name string
		in   func(in *servergen.Input)
	}{
		{"server", func(*servergen.Input) {}},
		{"job", func(in *servergen.Input) { in.Job = &servergen.JobInput{Name: "SyncItems"} }},
	} {
		t.Run("go/"+tc.name, func(t *testing.T) {
			in := servergen.Input{Stack: "vault-stack", Server: "vault-api", Dir: t.TempDir(), APIs: []servergen.APIInput{goAPI}, Naming: naming.Default()}
			tc.in(&in)
			s, err := servergen.Plan(in)
			if err != nil {
				t.Fatal(err)
			}
			if err := servergen.Write(s, in.Dir); err != nil {
				t.Fatal(err)
			}
			main, err := os.ReadFile(filepath.Join(in.Dir, servergen.MainFile))
			if err != nil {
				t.Fatal(err)
			}
			if want := "vaultDb, err := vaultdborm.ConnectWithPool(vaultDbPool, vaultdborm.WithFieldKeys(vaultApiConfig.VaultDbFieldKeys))"; !strings.Contains(string(main), want) {
				t.Errorf("main.go does not hold %q:\n%s", want, main)
			}
		})
	}

	tsAPI := servergen.TypeScriptAPIInput{
		Output: &tsrestgen.APIOutput{
			SchemaName:   "vault-api",
			PackageName:  "@schemas/vault-api-api",
			HasEnvConfig: true,
			Jobs:         []tsrestgen.JobInfo{{Name: "SyncItems", Method: "syncItems"}},
			Deps: tsrestgen.DepsInfo{
				Database: "vault-db", Dialect: "postgres", DatabaseTypesPackage: "@schemas/vault-db-types",
				ORM: "VaultDbOrm", ORMFieldKeys: "VAULT_DB_FIELD_KEYS",
			},
		},
		Routes:         &apigen.APIOutput{SchemaName: "vault-api"},
		Config:         config,
		Implementation: servergen.TypeScriptImplementation{Package: "@schemas/vault-api-implementation"},
	}
	for _, tc := range []struct {
		name string
		in   func(in *servergen.TypeScriptInput)
		file string
	}{
		{"server", func(*servergen.TypeScriptInput) {}, servergen.TypeScriptMainFile},
		{"job", func(in *servergen.TypeScriptInput) { in.Job = &servergen.JobInput{Name: "SyncItems"} }, servergen.TypeScriptMainFile},
	} {
		t.Run("typescript/"+tc.name, func(t *testing.T) {
			in := servergen.TypeScriptInput{Stack: "vault-stack", Server: "vault-api", Dir: t.TempDir(), APIs: []servergen.TypeScriptAPIInput{tsAPI}}
			tc.in(&in)
			s, err := servergen.PlanTypeScript(in)
			if err != nil {
				t.Fatal(err)
			}
			if err := servergen.WriteTypeScript(s, in.Dir); err != nil {
				t.Fatal(err)
			}
			main, err := os.ReadFile(filepath.Join(in.Dir, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if want := "orm: createVaultDbOrm(vaultDbPool, { fieldKeys: vaultApiConfig.VAULT_DB_FIELD_KEYS }),"; !strings.Contains(string(main), want) {
				t.Errorf("%s does not hold %q:\n%s", tc.file, want, main)
			}
		})
	}
}
