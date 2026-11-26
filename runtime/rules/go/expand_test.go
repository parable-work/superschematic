package rules_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	rules "github.com/parable-work/superschematic/runtime/rules/go"
)

// TestExpandGolden holds the static archive's expand (D152) to every case
// of runtime/engine/testdata/rules/recurrence/expand.golden.json, a
// refused input to {"refused": reason}, as the Rust core and the wasm32
// module through @superschematic/rules are held, so the three return the
// same dates.
func TestExpandGolden(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "engine", "testdata", "rules", "recurrence", "expand.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Cases []struct {
			Name   string          `json:"name"`
			Input  json.RawMessage `json:"input"`
			Output json.RawMessage `json:"output"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	if len(golden.Cases) < 10 {
		t.Fatalf("%d cases", len(golden.Cases))
	}
	for _, c := range golden.Cases {
		t.Run(c.Name, func(t *testing.T) {
			out, err := rules.ExpandJSON(c.Input)
			if err != nil {
				var refused *rules.Error
				if !errors.As(err, &refused) || refused.Code != "invalid_input" || !strings.HasPrefix(refused.Message, "expand: ") {
					t.Fatal(err)
				}
				out, _ = json.Marshal(map[string]string{"refused": strings.TrimPrefix(refused.Message, "expand: ")})
			}
			var got, want any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(c.Output, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("core: %s\nfile: %s", out, c.Output)
			}
		})
	}
}

// TestExpandTyped reads an expansion into the binding's types.
func TestExpandTyped(t *testing.T) {
	expansion, err := rules.Expand(rules.ExpandInput{
		Anchor: "relative", Date: json.RawMessage(`"2026-10-31"`), Offset: "-P1D", TimeZone: "UTC",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(expansion.Occurrences) != 1 || expansion.Occurrences[0].Due != "2026-10-30T00:00" || expansion.Occurrences[0].Key != "2026-10-30T00:00" {
		t.Fatalf("%+v", expansion)
	}
	if _, err := rules.Expand(rules.ExpandInput{Anchor: "weekly"}); err == nil {
		t.Fatal("an anchor that is none of the three expanded")
	}
}
