package canonical

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// valueCase is one vector of runtime/versiongraph/testdata/canonical: a
// value of a class as Postgres renders it (postgres, from the SQL in sql
// under timeZone), and its canonical JSON, or the reason its rule refuses it.
type valueCase struct {
	Name      string `json:"name"`
	Class     string `json:"class"`
	SQL       string `json:"sql"`
	TimeZone  string `json:"timeZone"`
	Postgres  string `json:"postgres"`
	Canonical string `json:"canonical"`
	Error     string `json:"error"`
}

// rowCase is one row vector: a row as to_jsonb renders it, the classes of
// its columns, and its canonical row.
type rowCase struct {
	Name      string            `json:"name"`
	Columns   map[string]string `json:"columns"`
	SQL       string            `json:"sql"`
	TimeZone  string            `json:"timeZone"`
	Postgres  string            `json:"postgres"`
	Canonical string            `json:"canonical"`
	Error     string            `json:"error"`
}

func vectorFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "canonical", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no canonical vectors found")
	}
	return files
}

func readVectors(t *testing.T) ([]valueCase, []rowCase) {
	t.Helper()
	var values []valueCase
	var rows []rowCase
	for _, file := range vectorFiles(t) {
		text, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Cases []valueCase `json:"cases"`
			Rows  []rowCase   `json:"rows"`
		}
		if err := json.Unmarshal(text, &doc); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if len(doc.Cases)+len(doc.Rows) == 0 {
			t.Fatalf("%s has no cases", file)
		}
		stem := strings.TrimSuffix(filepath.Base(file), ".json")
		for _, c := range doc.Cases {
			if element := strings.TrimSuffix(strings.TrimSuffix(c.Class, "[]"), "[]"); element != stem {
				t.Fatalf("%s: case %q has class %s, which belongs in %s.json", file, c.Name, c.Class, element)
			}
		}
		values = append(values, doc.Cases...)
		rows = append(rows, doc.Rows...)
	}
	return values, rows
}

// TestValueVectors runs every value vector through Postgres: the canonical
// JSON must match byte for byte, a refused value must be refused, and a
// canonical value must come back unchanged.
func TestValueVectors(t *testing.T) {
	values, _ := readVectors(t)
	for _, c := range values {
		t.Run(c.Class+"/"+c.Name, func(t *testing.T) {
			got, err := Postgres(c.Class, json.RawMessage(c.Postgres))
			if c.Error != "" {
				var refused *Error
				if !errors.As(err, &refused) {
					t.Fatalf("Postgres(%s, %s) = %s, %v; want it refused (%s)", c.Class, c.Postgres, got, err, c.Error)
				}
				return
			}
			if err != nil {
				t.Fatalf("Postgres(%s, %s): %v", c.Class, c.Postgres, err)
			}
			if string(got) != c.Canonical {
				t.Fatalf("Postgres(%s, %s)\n got: %s\nwant: %s", c.Class, c.Postgres, got, c.Canonical)
			}
			again, err := Postgres(c.Class, got)
			if err != nil || string(again) != c.Canonical {
				t.Fatalf("the canonical form is not a fixed point: Postgres(%s, %s) = %s, %v", c.Class, got, again, err)
			}
		})
	}
}

// TestEveryClassHasVectors fails when an element class, its list or its
// list of lists, or null, has no vector, or a class has no refused value.
func TestEveryClassHasVectors(t *testing.T) {
	values, _ := readVectors(t)
	seen := map[string]bool{}
	nulls := map[string]bool{}
	refused := map[string]bool{}
	for _, c := range values {
		seen[c.Class] = true
		element := strings.TrimSuffix(strings.TrimSuffix(c.Class, "[]"), "[]")
		if c.Postgres == "null" {
			nulls[element] = true
		}
		if c.Error != "" {
			refused[element] = true
		}
	}
	for element := range rules {
		if !seen[element] || !seen[element+"[]"] {
			t.Errorf("class %s needs a vector for a value and one for a list", element)
		}
		if !nulls[element] {
			t.Errorf("class %s needs a null vector", element)
		}
		if !refused[element] {
			t.Errorf("class %s needs a vector its rule refuses", element)
		}
	}
	for _, element := range []string{String, Integer, Number, UUID, DateTime, Duration, JSON} {
		if !seen[element+"[][]"] {
			t.Errorf("class %s[][] needs a vector", element)
		}
	}
}

// TestRowVectors runs every row vector through PostgresRow.
func TestRowVectors(t *testing.T) {
	_, rows := readVectors(t)
	if len(rows) == 0 {
		t.Fatal("no row vectors")
	}
	for _, c := range rows {
		t.Run(c.Name, func(t *testing.T) {
			got, err := PostgresRow(c.Columns, json.RawMessage(c.Postgres))
			if c.Error != "" {
				var refused *Error
				if !errors.As(err, &refused) {
					t.Fatalf("PostgresRow = %s, %v; want it refused (%s)", got, err, c.Error)
				}
				return
			}
			if err != nil {
				t.Fatalf("PostgresRow: %v", err)
			}
			if string(got) != c.Canonical {
				t.Fatalf("PostgresRow\n got: %s\nwant: %s", got, c.Canonical)
			}
		})
	}
}

func TestUnknownClassIsRefused(t *testing.T) {
	for _, class := range []string{"decimal", "uuid[][][]", "", "[]"} {
		if _, err := Postgres(class, json.RawMessage(`"x"`)); !errors.Is(err, ErrUnknownClass) {
			t.Errorf("Postgres(%q) = %v, want ErrUnknownClass", class, err)
		}
	}
	if _, err := PostgresRow(map[string]string{"id": "decimal"}, json.RawMessage(`{"id":1}`)); !errors.Is(err, ErrUnknownClass) {
		t.Errorf("PostgresRow with an unknown class = %v, want ErrUnknownClass", err)
	}
}

func TestInputThatIsNotOneJSONValueIsRefused(t *testing.T) {
	for _, input := range []string{``, `1 2`, `{"a":`} {
		if _, err := Postgres(Integer, json.RawMessage(input)); err == nil {
			t.Errorf("Postgres(integer, %q) accepted it", input)
		}
	}
	if _, err := PostgresRow(map[string]string{}, json.RawMessage(`[1]`)); err == nil {
		t.Error("PostgresRow accepted a row that is not an object")
	}
}

// TestNegativeZero covers -0, which no Postgres rendering carries (to_jsonb
// writes 0) but a stored JSON value can: it is 0 in both numeric classes.
func TestNegativeZero(t *testing.T) {
	for _, class := range []string{Integer, Number} {
		got, err := Postgres(class, json.RawMessage(`-0`))
		if err != nil || string(got) != "0" {
			t.Errorf("Postgres(%s, -0) = %s, %v; want 0", class, got, err)
		}
	}
}
