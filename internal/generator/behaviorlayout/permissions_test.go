package behaviorlayout_test

import (
	"testing"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
)

// The layout of a schema whose table composes Approvals and which has the
// identity runtime's role tables names those tables, where an adapter
// answers the core's permissions read (D200): the user table and its key,
// the credential's disabled and pending columns, the role grants and the
// roles' permissions. A schema whose Approvals have no identity tables
// names none.
func TestTheLayoutNamesWhereAPermissionsReadReads(t *testing.T) {
	layout := layoutOfFixture(t, "../../loader/tsreader/testdata/services/fixture-approvals-identity-db", "fixture-approvals-identity-db")
	want := behaviorlayout.Permissions{
		Users:           "account",
		User:            behaviorlayout.Column{Field: "id", Column: "id", Type: "Identity.UUID"},
		Credentials:     "user_credential",
		CredentialUser:  "user_id",
		DisabledAt:      "disabled_at",
		PendingAt:       "pending_at",
		Grants:          "user_role_grant",
		GrantUser:       "user_id",
		GrantRole:       "role_id",
		Roles:           "role",
		RoleKey:         "id",
		RolePermissions: "permissions",
	}
	if layout.Permissions == nil || *layout.Permissions != want {
		t.Fatalf("the layout's permissions = %+v, want %+v", layout.Permissions, want)
	}
	if other := layoutOfFixture(t, approvalsFixture, "fixture-approvals-db"); other.Permissions != nil {
		t.Fatalf("a schema with no identity tables names %+v", other.Permissions)
	}
}
