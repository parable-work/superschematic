package ir

import (
	"sort"
	"testing"
)

// TestIdentityOperationErrors: every operation of the user model has its
// error responses, in status order, each with a code and a meaning, and a
// caller's copy does not change the table.
func TestIdentityOperationErrors(t *testing.T) {
	ops := []string{
		IdentityOpLogin, IdentityOpLogout, IdentityOpMe, IdentityOpCapabilities, IdentityOpChangePassword, IdentityOpRegister,
		IdentityOpCreateUser, IdentityOpListUsers, IdentityOpGetUser, IdentityOpDisableUser, IdentityOpEnableUser, IdentityOpSetUserPassword,
		IdentityOpListRoles, IdentityOpCreateRole, IdentityOpUpdateRole, IdentityOpDeleteRole, IdentityOpGrantRole, IdentityOpRevokeRole,
	}
	if len(identityOperationErrors) != len(ops) {
		t.Errorf("the table has %d operations, want %d", len(identityOperationErrors), len(ops))
	}
	for _, op := range ops {
		errs := IdentityOperationErrors(op)
		if len(errs) == 0 {
			t.Errorf("%s has no error responses", op)
		}
		if !sort.SliceIsSorted(errs, func(i, j int) bool { return errs[i].Status < errs[j].Status }) {
			t.Errorf("%s's errors are not in status order: %+v", op, errs)
		}
		for _, e := range errs {
			if e.Code == "" || e.Meaning == "" || e.Status < 400 {
				t.Errorf("%s: %+v", op, e)
			}
		}
	}
	errs := IdentityOperationErrors(IdentityOpLogin)
	errs[0].Code = "changed"
	if IdentityOperationErrors(IdentityOpLogin)[0].Code != IdentityCodeInvalidCredentials {
		t.Error("a caller's copy changed the table")
	}
	if IdentityOperationErrors("greet") != nil {
		t.Error("an operation of the project has the user model's errors")
	}
}
