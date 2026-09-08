package generator

import (
	"reflect"
	"testing"
)

func TestParseOutputs(t *testing.T) {
	raw := map[string]any{
		"types": map[string]any{
			"go":         map[string]any{"enabled": true},
			"typescript": map[string]any{"enabled": true},
			"python":     map[string]any{"enabled": false},
		},
		"api": map[string]any{
			"enabled":            true,
			"scaffoldsOutputDir": "../../../services/web-api/internal/route-impl",
		},
		"sdk": map[string]any{
			"typescript": map[string]any{"enabled": true},
		},
	}

	outputs, err := ParseOutputs(raw)
	if err != nil {
		t.Fatalf("ParseOutputs: %v", err)
	}

	if got := outputs.EnabledTypeLanguages(); !reflect.DeepEqual(got, []string{"go", "typescript"}) {
		t.Errorf("EnabledTypeLanguages = %v, want [go typescript]", got)
	}
	if !outputs.TypesEnabled("go") || outputs.TypesEnabled("python") || outputs.TypesEnabled("rust") {
		t.Errorf("TypesEnabled flags wrong: %+v", outputs.Types)
	}
	if !outputs.APIEnabled() {
		t.Error("APIEnabled = false, want true")
	}
	if outputs.API.ScaffoldsOutputDir != "../../../services/web-api/internal/route-impl" {
		t.Errorf("ScaffoldsOutputDir = %q", outputs.API.ScaffoldsOutputDir)
	}
	if got := outputs.EnabledSDKLanguages(); !reflect.DeepEqual(got, []string{"typescript"}) {
		t.Errorf("EnabledSDKLanguages = %v, want [typescript]", got)
	}
}

func TestParseOutputsEmpty(t *testing.T) {
	outputs, err := ParseOutputs(nil)
	if err != nil {
		t.Fatalf("ParseOutputs(nil): %v", err)
	}
	if outputs.APIEnabled() || len(outputs.EnabledTypeLanguages()) != 0 || len(outputs.EnabledSDKLanguages()) != 0 {
		t.Errorf("empty outputs should enable nothing: %+v", outputs)
	}
}

func TestParseOutputsRejectsUnknownKey(t *testing.T) {
	_, err := ParseOutputs(map[string]any{"graphql": map[string]any{"enabled": true}})
	if err == nil {
		t.Fatal("expected error for unknown outputs key")
	}
}

func TestParseOutputsRejectsUnknownLanguage(t *testing.T) {
	_, err := ParseOutputs(map[string]any{
		"types": map[string]any{"cobol": map[string]any{"enabled": true}},
	})
	if err == nil {
		t.Fatal("expected error for unknown target language")
	}

	_, err = ParseOutputs(map[string]any{
		"sdk": map[string]any{"cobol": map[string]any{"enabled": true}},
	})
	if err == nil {
		t.Fatal("expected error for unknown SDK target language")
	}
}
