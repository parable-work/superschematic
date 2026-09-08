package yamlreader

import (
	"strings"
	"testing"
)

func TestReadSingleEnumWithComments(t *testing.T) {
	src := `# Runtime environment classification.
name: FixtureEnvironment
kind: Enum
values:
  # The local development environment.
  - name: Development
    serializedAs: development
  - name: Production # Deployed to customers.
    serializedAs: production
`
	doc, err := Read([]byte(src), "fixture-environment.schema.yaml")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	enum, ok := doc.Enums["FixtureEnvironment"]
	if !ok {
		t.Fatalf("no FixtureEnvironment enum decoded")
	}
	if enum.Comment != "Runtime environment classification." {
		t.Errorf("enum comment = %q", enum.Comment)
	}
	if enum.Values[0].Comment != "The local development environment." {
		t.Errorf("head comment on value[0] = %q", enum.Values[0].Comment)
	}
	if enum.Values[1].Comment != "Deployed to customers." {
		t.Errorf("line comment on value[1] = %q", enum.Values[1].Comment)
	}
}

func TestReadSingleTypeWithComments(t *testing.T) {
	src := `# A tenant of the platform.
name: Tenant
role: DBTable
fields:
  - name: id
    typeRef: { name: Identity.UUID }
    required: true
    key: true
  # Display name shown across the product.
  - name: name
    typeRef: { name: Identity.Name }
    required: true
    searchField: true
`
	doc, err := Read([]byte(src), "tenant.schema.yaml")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	def := doc.Types["Tenant"]
	if def == nil {
		t.Fatalf("no Tenant type decoded")
	}
	if def.Comment != "A tenant of the platform." {
		t.Errorf("type comment = %q", def.Comment)
	}
	if def.Fields[1].Comment != "Display name shown across the product." {
		t.Errorf("field comment = %q", def.Fields[1].Comment)
	}
	if !def.Fields[0].Key || !def.Fields[1].SearchField {
		t.Errorf("field decorators lost: %+v, %+v", def.Fields[0], def.Fields[1])
	}
}

func TestReadMultiDefinitionDocumentWithComments(t *testing.T) {
	src := `# Shared fixture documents.
kind: General
scalars:
  Network.Url:
    name: Network.Url
    languagePrimitive: string
types:
  # Service configuration block.
  FixtureConfig:
    name: FixtureConfig
    role: EmbeddedStruct
    envVars: true
    fields:
      - name: DATABASE_URL
        typeRef: { name: Network.Url }
        required: true
enums:
  Status:
    name: Status
    comment: Explicit comment key.
    values:
      - name: Active
`
	doc, err := Read([]byte(src), "fixture.schema.yaml")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if doc.Comment != "Shared fixture documents." {
		t.Errorf("document comment = %q", doc.Comment)
	}
	def := doc.Types["FixtureConfig"]
	if def == nil {
		t.Fatalf("no FixtureConfig type decoded")
	}
	if def.Comment != "Service configuration block." {
		t.Errorf("type comment = %q", def.Comment)
	}
	// Explicit comment keys are IR fields and survive when no native
	// comment overrides them.
	if doc.Enums["Status"].Comment != "Explicit comment key." {
		t.Errorf("explicit comment = %q", doc.Enums["Status"].Comment)
	}
}

func TestReadRejectsUnknownKeys(t *testing.T) {
	src := `name: Tenant
role: DBTable
sparkles: true
`
	_, err := Read([]byte(src), "tenant.schema.yaml")
	if err == nil {
		t.Fatal("Read accepted an unknown key")
	}
	if !strings.Contains(err.Error(), "sparkles") {
		t.Errorf("error %q does not mention the unknown key", err.Error())
	}
}
