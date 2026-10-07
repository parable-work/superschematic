package naming

import (
	"reflect"
	"testing"

	"github.com/parable-work/superschematic/internal/release"
)

// TestReleasePins: a release pins each runtime Go module no [paths] key
// names a checkout of, superschematic's at its tag and superscalar's at the
// version it links; a checkout's binary, a module the naming file renames
// and a superscalar version the binary does not know pin nothing.
func TestReleasePins(t *testing.T) {
	d := Default()
	rel := release.Release{Version: "1.2.3", ScalarGo: "v0.0.0-20260928143325-10cf493f485e"}
	all := Pins{
		d.SchemaIRGoModule:      "v1.2.3",
		d.SchemaRuntimeGoModule: "v1.2.3",
		d.HTTPRuntimeGoModule:   "v1.2.3",
		d.VersionGraphGoModule:  "v1.2.3",
		d.ScalarGoModule:        "v0.0.0-20260928143325-10cf493f485e",
	}
	without := func(modules ...string) Pins {
		out := Pins{}
		for module, version := range all {
			out[module] = version
		}
		for _, module := range modules {
			delete(out, module)
		}
		return out
	}
	renamed := d
	renamed.HTTPRuntimeGoModule = "example.com/runtime/http"
	renamed.ScalarGoModule = "example.com/scalars/go"

	for _, tc := range []struct {
		name   string
		naming Naming
		paths  LocalPaths
		rel    release.Release
		want   Pins
	}{
		{"a checkout's binary", d, LocalPaths{}, release.Release{ScalarGo: rel.ScalarGo}, nil},
		{"no [paths]", d, LocalPaths{}, rel, all},
		{"an empty naming file", Naming{}, LocalPaths{}, rel, all},
		{"[paths] names checkouts", d, LocalPaths{ScalarGo: "/repo/third_party/superscalar/go", SchemaIR: "/repo/ir"}, rel, without(d.ScalarGoModule, d.SchemaIRGoModule)},
		{"every [paths] key", d, LocalPaths{
			ScalarGo: "/s", SchemaIR: "/i", SchemaRuntimeGo: "/r", HTTPRuntimeGo: "/h", VersionGraphGo: "/v",
		}, rel, Pins{}},
		{"renamed modules", renamed, LocalPaths{}, rel, without(d.HTTPRuntimeGoModule, d.ScalarGoModule)},
		{"no superscalar version", d, LocalPaths{}, release.Release{Version: "1.2.3"}, without(d.ScalarGoModule)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.naming.ReleasePins(tc.paths, tc.rel); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ReleasePins = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPinsOfVersionAndList: Of keeps the pinned modules among those named,
// Version falls back for a module with no pin, and List sorts.
func TestPinsOfVersionAndList(t *testing.T) {
	pins := Pins{"b.example/m": "v2.0.0", "a.example/m": "v1.0.0", "c.example/m": "v3.0.0"}
	of := pins.Of("c.example/m", "a.example/m", "z.example/unpinned")
	if want := (Pins{"a.example/m": "v1.0.0", "c.example/m": "v3.0.0"}); !reflect.DeepEqual(of, want) {
		t.Errorf("Of = %v, want %v", of, want)
	}
	if got := pins.Of("z.example/unpinned"); got != nil {
		t.Errorf("Of with no pinned module = %v, want nil", got)
	}
	if got := of.Version("a.example/m", "v0.0.0"); got != "v1.0.0" {
		t.Errorf("Version of a pinned module = %q, want v1.0.0", got)
	}
	if got := of.Version("b.example/m", "v0.0.0"); got != "v0.0.0" {
		t.Errorf("Version of a module Of left out = %q, want v0.0.0", got)
	}
	if got := Pins(nil).Version("a.example/m", "v0.0.0"); got != "v0.0.0" {
		t.Errorf("Version with no pins = %q, want v0.0.0", got)
	}
	if got, want := pins.List(), []Pin{{"a.example/m", "v1.0.0"}, {"b.example/m", "v2.0.0"}, {"c.example/m", "v3.0.0"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}
}
