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

// TestReadUserTraits: the YAML form carries the user model's traits (D50)
// as the JSON form does, userRole as an empty mapping.
func TestReadUserTraits(t *testing.T) {
	src := `kind: DB
types:
  Account:
    name: Account
    role: DBTable
    user: { login: handle }
  Role:
    name: Role
    role: DBTable
    userRole: {}
`
	doc, err := Read([]byte(src), "accounts.schema.yaml")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if user := doc.Types["Account"].User; user == nil || user.Login != "handle" || user.Name != "" || user.NameField() != "handle" {
		t.Errorf("Account.User = %+v", user)
	}
	if doc.Types["Role"].UserRole == nil {
		t.Error("Role.UserRole is nil")
	}

	if _, err := Read([]byte("name: Role\nrole: DBTable\nuserRole: true\n"), "role.schema.yaml"); err == nil {
		t.Error("Read accepted a userRole that is not a mapping")
	}
}

// TestReadUserRoutes: the YAML form carries the user model's route sets
// (D50) as the JSON form does, an empty config as an empty mapping.
func TestReadUserRoutes(t *testing.T) {
	src := `kind: API
operationSets:
  - name: Account
    operations: []
    userSessions: { register: true }
  - name: AccountAdmin
    operations: []
    userAdministration: { path: staff/admin }
  - name: Me
    operations: []
    userSessions: {}
`
	doc, err := Read([]byte(src), "account.schema.yaml")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	sets := doc.OperationSets
	if len(sets) != 3 {
		t.Fatalf("operation sets = %+v", sets)
	}
	if cfg := sets[0].UserSessions; cfg == nil || !cfg.Register || cfg.NoLogin || cfg.Path != "" {
		t.Errorf("Account.UserSessions = %+v", cfg)
	}
	if cfg := sets[1].UserAdministration; cfg == nil || cfg.Path != "staff/admin" {
		t.Errorf("AccountAdmin.UserAdministration = %+v", cfg)
	}
	if cfg := sets[2].UserSessions; cfg == nil || cfg.Register || cfg.NoLogin {
		t.Errorf("Me.UserSessions = %+v", cfg)
	}

	if _, err := Read([]byte("kind: OperationSet\nname: Account\noperations: []\nuserSessions: { roles: true }\n"), "account.schema.yaml"); err == nil {
		t.Error("Read accepted a userSessions key the config does not take")
	}
}
