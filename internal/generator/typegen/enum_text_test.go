package typegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// enumTextOutput is fixture-general with two enums: UploadProblem, whose
// members' comments carry `title:` and `action:` lines (one member has
// neither, one only a title, and one spells a key in upper case), and
// Shade, whose comments carry none.
func enumTextOutput(t *testing.T, n naming.Naming) *ModuleOutput {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-general"))
	if err != nil {
		t.Fatal(err)
	}
	schema.Enums["UploadProblem"] = &ir.EnumDef{Name: "UploadProblem", Values: []ir.EnumValueDef{
		{Name: "TOO_LARGE", SerializedAs: "UP-001", Comment: "title: The upload was refused\naction: Pick a smaller file and try again.\nThe file is larger than the bucket takes."},
		{Name: "WRONG_TYPE", SerializedAs: "UP-002", Comment: "TITLE:   The file type is not allowed  \nThe bucket takes images only."},
		{Name: "UNKNOWN", SerializedAs: "UP-003", Comment: "No words for a person."},
	}}
	schema.Enums["Shade"] = &ir.EnumDef{Name: "Shade", Values: []ir.EnumValueDef{
		{Name: "Light", SerializedAs: "light", Comment: "A light shade."},
		{Name: "Dark", SerializedAs: "dark"},
	}}
	output, err := Generate(schema, Options{
		SchemaName: "fixture-general",
		ModulePath: "example.com/schemas/types/go/enum-text",
		Naming:     n,
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	return output
}

// TestEnumTextMethodsAreOffByDefault: without go.enum_text_methods no enum
// gets Title or Action.
func TestEnumTextMethodsAreOffByDefault(t *testing.T) {
	dir := t.TempDir()
	if err := WriteTypes(enumTextOutput(t, naming.Naming{}), dir); err != nil {
		t.Fatal(err)
	}
	enums, err := os.ReadFile(filepath.Join(dir, "enums.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(enums), ") Title() string") || strings.Contains(string(enums), ") Action() string") {
		t.Fatal("enums.go writes Title or Action with the option off")
	}
}

// TestEnumTextMethodsReturnTheCommentLines: with go.enum_text_methods =
// "on" an enum whose members' comments carry a `title:` or an `action:`
// line gets Title() and Action(), which return each member's line, trimmed,
// and "" for a member without one; the key matches in any case and the
// comment keeps the lines. An enum with no such line gets neither method.
// A test inside the generated module calls them.
func TestEnumTextMethodsReturnTheCommentLines(t *testing.T) {
	output := enumTextOutput(t, naming.Naming{Go: naming.GoConfig{EnumTextMethods: naming.GoOn}})
	for _, enum := range output.Enums {
		if want := enum.Name == "UploadProblem"; enum.TextMethods != want {
			t.Errorf("%s: TextMethods = %t, want %t", enum.Name, enum.TextMethods, want)
		}
	}
	code := `package types

import (
	"reflect"
	"testing"
)

func TestEnumText(t *testing.T) {
	for member, want := range map[UploadProblem][2]string{
		UploadProblem_TOO_LARGE:  {"The upload was refused", "Pick a smaller file and try again."},
		UploadProblem_WRONG_TYPE: {"The file type is not allowed", ""},
		UploadProblem_UNKNOWN:    {"", ""},
		UploadProblem("UP-999"):  {"", ""},
	} {
		if got := [2]string{member.Title(), member.Action()}; got != want {
			t.Errorf("%s: Title, Action = %q, want %q", member, got, want)
		}
	}
	if _, ok := reflect.TypeFor[Shade]().MethodByName("Title"); ok {
		t.Error("Shade has Title, though no member's comment has a title: line")
	}
}
`
	runGeneratedModuleTest(t, output, "enum_text", code)
	dir := t.TempDir()
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	enums, err := os.ReadFile(filepath.Join(dir, "enums.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(enums), "UploadProblemTOO_LARGE - title: The upload was refused") {
		t.Error("the member's comment lost its title: line")
	}
}
