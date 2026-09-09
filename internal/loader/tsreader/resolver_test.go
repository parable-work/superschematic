package tsreader

import (
	"path/filepath"
	"testing"
)

func TestRecordTypeLoadsAsTypedMap(t *testing.T) {
	schema, _, err := LoadService(filepath.Join("testdata", "services", "fixture-maps"))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}

	container := schema.Types["MapContainer"]
	if container == nil {
		t.Fatal("MapContainer not loaded")
	}
	if len(container.Fields) != 2 {
		t.Fatalf("MapContainer fields = %d, want 2", len(container.Fields))
	}

	strings := container.Fields[0]
	if !strings.TypeRef.IsMap || strings.TypeRef.Name != "string" {
		t.Fatalf("strings TypeRef = %+v, want string map", strings.TypeRef)
	}
	nested := container.Fields[1]
	if !nested.TypeRef.IsMap || nested.TypeRef.Name != "MapValue" {
		t.Fatalf("nested TypeRef = %+v, want MapValue map", nested.TypeRef)
	}
}

func TestScalarSQLTypeMappingUsesScalarLibMetadata(t *testing.T) {
	tests := []struct {
		brand string
		want  string
	}{
		{brand: "Contact.Email", want: "CITEXT"},
		{brand: "Network.IpAddress", want: "INET"},
	}

	for _, tt := range tests {
		t.Run(tt.brand, func(t *testing.T) {
			got := scalarSQLTypeMapping(tt.brand)
			if got["sql"] != tt.want {
				t.Fatalf("scalarSQLTypeMapping(%q)[sql] = %q, want %q", tt.brand, got["sql"], tt.want)
			}
		})
	}
}
