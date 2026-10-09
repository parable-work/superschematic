package jsonreader

import (
	"strings"
	"testing"
)

// TestReadRuntimeModePayload exercises the runtime-mode surface: a schema
// arriving as bytes (a persisted row, an over-the-wire body) validates
// through the same pipeline as a build-time file.
func TestReadRuntimeModePayload(t *testing.T) {
	payload := []byte(`{
		"types": {
			"SavedView": {
				"name": "SavedView",
				"role": "EmbeddedStruct",
				"comment": "A customer-saved view definition.",
				"fields": [
					{"name": "title", "typeRef": {"name": "string"}, "required": true}
				]
			}
		}
	}`)
	doc, err := Read(payload, "saved-view-row")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	def := doc.Types["SavedView"]
	if def == nil {
		t.Fatal("decoded document has no SavedView type")
	}
	if def.Comment != "A customer-saved view definition." {
		t.Errorf("comment = %q", def.Comment)
	}
}

func TestReadRejectsInvalidRuntimePayload(t *testing.T) {
	payload := []byte(`{"types": {"X": {"name": "X", "role": "NotARole"}}}`)
	_, err := Read(payload, "saved-view-row")
	if err == nil {
		t.Fatal("Read accepted an invalid payload")
	}
	if !strings.Contains(err.Error(), "saved-view-row") {
		t.Errorf("error %q does not carry the runtime source label", err.Error())
	}
}

func TestReadFileMissing(t *testing.T) {
	if _, err := ReadFile("does/not/exist.schema.json", "exist.schema.json"); err == nil {
		t.Fatal("ReadFile succeeded on a missing file")
	}
}

// TestReadUserTraits: a type carries the user model's traits (D50) as
// user: { login, name? } and userRole: {}, and the JSON Schema closes both.
func TestReadUserTraits(t *testing.T) {
	doc, err := Read([]byte(`{
		"kind": "DB",
		"types": {
			"Account": {"name": "Account", "role": "DBTable", "user": {"login": "email", "name": "displayName"}},
			"Role": {"name": "Role", "role": "DBTable", "userRole": {}}
		}
	}`), "accounts.schema.json")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if user := doc.Types["Account"].User; user == nil || user.Login != "email" || user.Name != "displayName" {
		t.Errorf("Account.User = %+v", user)
	}
	if doc.Types["Role"].UserRole == nil {
		t.Error("Role.UserRole is nil")
	}

	for _, tc := range []struct{ payload, want string }{
		{`{"name": "Account", "role": "DBTable", "user": {"name": "displayName"}}`, "missing property 'login'"},
		{`{"name": "Account", "role": "DBTable", "user": {"login": "email", "field": "x"}}`, "'field' not allowed"},
		{`{"name": "Role", "role": "DBTable", "userRole": {"name": "x"}}`, "'name' not allowed"},
	} {
		if _, err := Read([]byte(tc.payload), "accounts.schema.json"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Read(%s) error = %v, want one naming %s", tc.payload, err, tc.want)
		}
	}
}

// TestReadUserRoutes: an operation set carries the user model's route
// sets (D50) as userSessions: { path?, noLogin?, register? } and
// userAdministration: { path? }, which the JSON Schema closes.
func TestReadUserRoutes(t *testing.T) {
	doc, err := Read([]byte(`{
		"kind": "API",
		"operationSets": [
			{"name": "Account", "operations": [], "userSessions": {"path": "account", "register": true}},
			{"name": "Me", "operations": [], "userSessions": {"noLogin": true}},
			{"name": "AccountAdmin", "operations": [], "userAdministration": {}}
		]
	}`), "account.schema.json")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	sets := doc.OperationSets
	if len(sets) != 3 {
		t.Fatalf("operation sets = %+v", sets)
	}
	if cfg := sets[0].UserSessions; cfg == nil || cfg.Path != "account" || !cfg.Register || cfg.NoLogin {
		t.Errorf("Account.UserSessions = %+v", cfg)
	}
	if cfg := sets[1].UserSessions; cfg == nil || !cfg.NoLogin || cfg.UserSessionsPath() != "auth" {
		t.Errorf("Me.UserSessions = %+v", cfg)
	}
	if cfg := sets[2].UserAdministration; cfg == nil || cfg.UserAdministrationPath() != "auth/admin" || sets[2].UserSessions != nil {
		t.Errorf("AccountAdmin = %+v", sets[2])
	}

	for _, tc := range []struct{ payload, want string }{
		{`{"kind": "OperationSet", "name": "Account", "operations": [], "userSessions": {"login": false}}`, "'login' not allowed"},
		{`{"kind": "OperationSet", "name": "Admin", "operations": [], "userAdministration": {"register": true}}`, "'register' not allowed"},
		{`{"kind": "OperationSet", "name": "Account", "operations": [], "userSessions": true}`, "want object"},
	} {
		if _, err := Read([]byte(tc.payload), "account.schema.json"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Read(%s) error = %v, want one naming %s", tc.payload, err, tc.want)
		}
	}
}
