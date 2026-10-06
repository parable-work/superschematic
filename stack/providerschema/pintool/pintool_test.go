package pintool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/stack/providerschema"
)

// The fixture provider: one resource with a renamed scalar, a list the
// bridge pluralized, a block of at most one with a nested pluralized list,
// a map and an enum, and descriptions the tool leaves out.
const fixtureSchema = `{
  "name": "gcp",
  "resources": {
    "gcp:test/widget:Widget": {
      "description": "a widget",
      "inputProperties": {
        "name": {"type": "string", "description": "its name"},
        "invokerIamDisabled": {"type": "boolean"},
        "labels": {"type": "object", "additionalProperties": {"type": "string"}},
        "kind": {"$ref": "#/types/gcp:test/WidgetKind:WidgetKind"},
        "envs": {"type": "array", "items": {"$ref": "#/types/gcp:test/WidgetEnv:WidgetEnv"}},
        "template": {"$ref": "#/types/gcp:test/WidgetTemplate:WidgetTemplate", "deprecationMessage": "use spec"}
      },
      "requiredInputs": ["template", "name"]
    },
    "gcp:test/other:Other": {"inputProperties": {}}
  },
  "types": {
    "gcp:test/WidgetKind:WidgetKind": {"type": "string", "enum": [{"name": "Big", "value": "BIG", "description": "big"}]},
    "gcp:test/WidgetEnv:WidgetEnv": {"type": "object", "properties": {"name": {"type": "string"}, "valueSource": {"type": "string"}}, "required": ["name"]},
    "gcp:test/WidgetTemplate:WidgetTemplate": {"type": "object", "properties": {"containers": {"type": "array", "items": {"$ref": "#/types/gcp:test/WidgetContainer:WidgetContainer"}}}},
    "gcp:test/WidgetContainer:WidgetContainer": {"type": "object", "properties": {"commands": {"type": "array", "items": {"type": "string"}}, "imageUri": {"type": "string"}}},
    "gcp:test/Unused:Unused": {"type": "object", "properties": {}}
  }
}`

const fixtureMetadata = `{
  "auto-aliasing": {
    "resources": {
      "google_test_widget": {
        "current": "gcp:test/widget:Widget",
        "fields": {
          "env": {"maxItemsOne": false},
          "template": {"maxItemsOne": true, "elem": {"fields": {"containers": {"maxItemsOne": false, "elem": {"fields": {"command": {"maxItemsOne": false}}}}}}}
        }
      },
      "google_test_other": {"current": "gcp:test/other:Other"}
    }
  }
}`

// pinFile is the fixture's pin file, of package gcp.
const pinFile = "pulumi-gcp.json"

// fixture writes a pin for the widget at version 1.0.0 into a new
// directory and returns it with a fetcher that serves the fixture files.
func fixture(t *testing.T, schema, metadata string) (string, Fetcher) {
	t.Helper()
	dir := t.TempDir()
	pin := `{"package": "gcp", "version": "1.0.0", "sources": [], "types": ["gcp:test/widget:Widget"]}`
	if err := os.WriteFile(filepath.Join(dir, pinFile), []byte(pin), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, func(url string) ([]byte, error) {
		if !strings.Contains(url, "/v1.0.0/") {
			t.Errorf("fetched %s, not at the pinned version", url)
		}
		if !strings.HasPrefix(url, "https://raw.githubusercontent.com/pulumi/pulumi-gcp/") || !strings.Contains(url, "/provider/cmd/pulumi-resource-gcp/") {
			t.Errorf("fetched %s, not from the gcp provider's repository", url)
		}
		if strings.HasSuffix(url, "/"+schemaFile) {
			return []byte(schema), nil
		}
		return []byte(metadata), nil
	}
}

func TestExtract(t *testing.T) {
	dir, fetch := fixture(t, fixtureSchema, fixtureMetadata)
	if err := Run(dir, pinFile, "", false, fetch); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "test.widget.Widget.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "description") || strings.Contains(string(data), "Unused") {
		t.Errorf("the pinned file keeps descriptions or an unreferenced type:\n%s", data)
	}
	var s providerschema.Schema
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	if s.Token != "gcp:test/widget:Widget" || s.Terraform.Type != "google_test_widget" {
		t.Errorf("token %s is Terraform %s", s.Token, s.Terraform.Type)
	}
	wantRenames := map[string]string{
		"invokerIamDisabled":           "invoker_iam_disabled",
		"envs":                         "env",
		"envs.valueSource":             "value_source",
		"template.containers.commands": "command",
		"template.containers.imageUri": "image_uri",
	}
	if !reflect.DeepEqual(s.Terraform.Renames, wantRenames) {
		t.Errorf("renames = %v, want %v", s.Terraform.Renames, wantRenames)
	}
	var types []string
	for token := range s.Types {
		types = append(types, token)
	}
	if len(types) != 4 {
		t.Errorf("types = %v, want the four the inputs reach", types)
	}
	if got := strings.Join(s.RequiredInputs, ","); got != "name,template" {
		t.Errorf("required inputs = %s", got)
	}
	if p := s.Property("template"); p == nil || p.DeprecationMessage != "use spec" {
		t.Errorf("template = %+v, want its deprecation kept", p)
	}
	if p := s.Property("template", "containers", "commands"); p == nil || p.Type != "array" {
		t.Errorf("template.containers.commands = %+v", p)
	}

	var pin providerschema.Pin
	pinData, err := os.ReadFile(filepath.Join(dir, pinFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(pinData, &pin); err != nil {
		t.Fatal(err)
	}
	if len(pin.Sources) != 2 || pin.Sources[0].Name != schemaFile || len(pin.Sources[0].SHA256) != 64 {
		t.Errorf("pin sources = %+v, want both files by digest", pin.Sources)
	}

	// The schema compiles, and the files check.
	if _, err := s.JSONSchema(); err != nil {
		t.Error(err)
	}
	if err := Run(dir, pinFile, "", true, fetch); err != nil {
		t.Errorf("-check after a write: %v", err)
	}
}

func TestCheckFindsDrift(t *testing.T) {
	dir, fetch := fixture(t, fixtureSchema, fixtureMetadata)
	if err := Run(dir, pinFile, "", false, fetch); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "test.widget.Widget.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Run(dir, pinFile, "", true, fetch); err == nil {
		t.Error("-check passed an edited file")
	}
	if err := Run(dir, pinFile, "", false, fetch); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "test.other.Other.json")
	if err := os.WriteFile(stale, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Run(dir, pinFile, "", true, fetch); err == nil {
		t.Error("-check passed a file for a type the pin does not list")
	}
	if err := Run(dir, pinFile, "", false, fetch); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a write kept a file for a type the pin does not list")
	}
}

func TestDigestMismatch(t *testing.T) {
	dir, fetch := fixture(t, fixtureSchema, fixtureMetadata)
	if err := Run(dir, pinFile, "", false, fetch); err != nil {
		t.Fatal(err)
	}
	changed := func(url string) ([]byte, error) {
		data, err := fetch(url)
		if strings.HasSuffix(url, "/"+schemaFile) {
			data = append(data, ' ')
		}
		return data, err
	}
	for _, check := range []bool{true, false} {
		if err := Run(dir, pinFile, "", check, changed); err == nil || !strings.Contains(err.Error(), "records") {
			t.Errorf("check=%v: err = %v, want a digest mismatch", check, err)
		}
	}
}

func TestUnmatchedList(t *testing.T) {
	metadata := strings.Replace(fixtureMetadata, `"env": {"maxItemsOne": false},`, "", 1)
	dir, fetch := fixture(t, fixtureSchema, metadata)
	err := Run(dir, pinFile, "", false, fetch)
	if err == nil || !strings.Contains(err.Error(), "property envs") {
		t.Fatalf("err = %v, want envs refused for want of a list field", err)
	}
}

// TestPinNamesItsPackage refuses a pin whose package is not the one its
// file name says, since the package picks the repository fetched from.
func TestPinNamesItsPackage(t *testing.T) {
	dir, fetch := fixture(t, fixtureSchema, fixtureMetadata)
	if err := os.Rename(filepath.Join(dir, pinFile), filepath.Join(dir, "pulumi-cloudflare.json")); err != nil {
		t.Fatal(err)
	}
	err := Run(dir, "pulumi-cloudflare.json", "", false, fetch)
	if err == nil || !strings.Contains(err.Error(), `pins package "gcp"`) {
		t.Errorf("err = %v, want the package refused", err)
	}
}

func TestVersionMovesThePin(t *testing.T) {
	dir, fetch := fixture(t, fixtureSchema, fixtureMetadata)
	if err := Run(dir, pinFile, "", false, fetch); err != nil {
		t.Fatal(err)
	}
	var fetched []string
	moved := func(url string) ([]byte, error) {
		fetched = append(fetched, url)
		return fetch(strings.Replace(url, "/v2.0.0/", "/v1.0.0/", 1))
	}
	if err := Run(dir, pinFile, "2.0.0", false, moved); err != nil {
		t.Fatal(err)
	}
	if len(fetched) != 2 || !strings.Contains(fetched[0], "/v2.0.0/") {
		t.Errorf("fetched %v, want both files at v2.0.0", fetched)
	}
	data, err := os.ReadFile(filepath.Join(dir, pinFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"version": "2.0.0"`) {
		t.Errorf("the pin is not at 2.0.0:\n%s", data)
	}
	if err := Run(dir, pinFile, "3.0.0", true, moved); err == nil {
		t.Error("-check with -version passed")
	}
}

func TestNames(t *testing.T) {
	for in, want := range map[string]string{"invokerIamDisabled": "invoker_iam_disabled", "name": "name", "ipv4Enabled": "ipv4_enabled"} {
		if got := toSnake(in); got != want {
			t.Errorf("toSnake(%s) = %s, want %s", in, got, want)
		}
	}
	if got := toCamel("depends_on"); got != "dependsOn" {
		t.Errorf("toCamel(depends_on) = %s", got)
	}
	if got := plurals("policy"); !reflect.DeepEqual(got, []string{"policys", "policyes", "policies"}) {
		t.Errorf("plurals(policy) = %v", got)
	}
}
