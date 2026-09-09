package session_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/parable-work/superschematic/runtime/http/go/session"
)

func TestCoversIsEqualOrNestedUnderDot(t *testing.T) {
	cases := []struct {
		granted, required string
		want              bool
	}{
		{"a", "a", true},
		{"a", "a.b", true},
		{"a", "a.b.c", true},
		{"a.b", "a", false},
		{"a", "ab", false},
		{"admin", "billing.view", false},
	}
	for _, tc := range cases {
		if got := session.Covers(tc.granted, tc.required); got != tc.want {
			t.Errorf("Covers(%q, %q) = %v, want %v", tc.granted, tc.required, got, tc.want)
		}
	}
}

func TestHasAnyPermissionHasNoRootOverride(t *testing.T) {
	if !session.HasAnyPermission([]string{"reports"}, []string{"reports.export", "billing"}) {
		t.Fatal("reports must cover reports.export")
	}
	if session.HasAnyPermission([]string{"admin"}, []string{"reports.export"}) {
		t.Fatal("no permission covers everything in the generic model")
	}
	if !session.HasAnyPermission(nil, nil) {
		t.Fatal("an empty required set is always satisfied")
	}
	roles := []session.Role{{Name: "viewer", Permissions: []string{"reports.view"}}, {Name: "ops", Permissions: []string{"deploy"}}}
	if !session.AnyRoleCoversAny(roles, []string{"deploy.prod"}) || session.AnyRoleCovers(roles, "reports.export") {
		t.Fatalf("role coverage wrong for %+v", roles)
	}
}

func TestContextHelpersRoundTripAndReportAbsence(t *testing.T) {
	ctx := context.Background()
	if session.HasPrincipalID(ctx) || session.GetPrincipalName(ctx) != "" || session.GetSessionID(ctx) != "" || session.GetRoles(ctx) != nil {
		t.Fatal("empty context must report nothing set")
	}
	roles := []session.Role{{ID: "r1", Name: "viewer", Permissions: []string{"reports.view"}}}
	ctx = session.ContextWithPrincipalID(ctx, "p1")
	ctx = session.ContextWithPrincipalName(ctx, "Ada")
	ctx = session.ContextWithSessionID(ctx, "s1")
	ctx = session.ContextWithRoles(ctx, roles)
	if !session.HasPrincipalID(ctx) || session.GetPrincipalID(ctx) != "p1" {
		t.Fatalf("principal id = %q", session.GetPrincipalID(ctx))
	}
	if session.GetPrincipalName(ctx) != "Ada" || session.GetSessionID(ctx) != "s1" {
		t.Fatalf("name/session = %q/%q", session.GetPrincipalName(ctx), session.GetSessionID(ctx))
	}
	if got := session.GetRoles(ctx); len(got) != 1 || got[0].ID != "r1" {
		t.Fatalf("roles = %+v", got)
	}
	if session.HasPrincipalID(session.ContextWithPrincipalID(context.Background(), "")) {
		t.Fatal("an empty principal id is unauthenticated")
	}
}

func TestBearerTokenParsesOnlyNonEmptyBearer(t *testing.T) {
	h := http.Header{}
	if _, ok := session.BearerToken(h); ok {
		t.Fatal("missing header must not parse")
	}
	h.Set("Authorization", "Bearer ")
	if _, ok := session.BearerToken(h); ok {
		t.Fatal("empty token must not parse")
	}
	h.Set("Authorization", "Basic abc")
	if _, ok := session.BearerToken(h); ok {
		t.Fatal("non-bearer scheme must not parse")
	}
	h.Set("Authorization", "Bearer tok-1")
	if tok, ok := session.BearerToken(h); !ok || tok != "tok-1" {
		t.Fatalf("BearerToken = %q, %v", tok, ok)
	}
}

func serve(mw func(http.Handler) http.Handler, ctx context.Context) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(rec, req)
	return rec
}

func TestRequireAuthAndPermissionsStatusCodes(t *testing.T) {
	anon := context.Background()
	viewer := session.ContextWithRoles(session.ContextWithPrincipalID(anon, "p1"), []session.Role{{Permissions: []string{"reports"}}})

	if rec := serve(session.RequireAuth, anon); rec.Code != http.StatusUnauthorized {
		t.Fatalf("RequireAuth anonymous = %d, want 401", rec.Code)
	}
	if rec := serve(session.RequireAuth, viewer); rec.Code != http.StatusNoContent {
		t.Fatalf("RequireAuth authenticated = %d, want 204", rec.Code)
	}
	if rec := serve(session.RequirePermissions("reports.export"), anon); rec.Code != http.StatusUnauthorized {
		t.Fatalf("RequirePermissions anonymous = %d, want 401", rec.Code)
	}
	if rec := serve(session.RequirePermissions("reports.export"), viewer); rec.Code != http.StatusNoContent {
		t.Fatalf("RequirePermissions covered = %d, want 204", rec.Code)
	}
	rec := serve(session.RequirePermissions("billing"), viewer)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("RequirePermissions uncovered = %d, want 403", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct == "" {
		t.Fatal("403 must carry the runtime's problem-detail body")
	}
}

func TestRequirePermissionsWithUsesCallerMatcher(t *testing.T) {
	root := session.ContextWithRoles(session.ContextWithPrincipalID(context.Background(), "p1"), []session.Role{{Permissions: []string{"root"}}})
	rootWins := func(roles []session.Role, _ []string) bool {
		return session.AnyRoleCovers(roles, "root")
	}
	if rec := serve(session.RequirePermissionsWith(rootWins, "anything"), root); rec.Code != http.StatusNoContent {
		t.Fatalf("custom matcher = %d, want 204", rec.Code)
	}
	if rec := serve(session.RequirePermissions("anything"), root); rec.Code != http.StatusForbidden {
		t.Fatalf("default matcher must not treat root as a wildcard, got %d", rec.Code)
	}
}

func TestGuardNilAllowedOnlyChecksAuthentication(t *testing.T) {
	always := func(context.Context) bool { return true }
	never := func(context.Context) bool { return false }
	if rec := serve(session.Guard(never, nil), context.Background()); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d, want 401", rec.Code)
	}
	if rec := serve(session.Guard(always, nil), context.Background()); rec.Code != http.StatusNoContent {
		t.Fatalf("authenticated, nil allowed = %d, want 204", rec.Code)
	}
	if rec := serve(session.Guard(always, never), context.Background()); rec.Code != http.StatusForbidden {
		t.Fatalf("authenticated, disallowed = %d, want 403", rec.Code)
	}
}
