package session

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

func TestGetCallerIsTheWholePrincipalOrAUserOrNoOne(t *testing.T) {
	ctx := context.Background()
	if got := GetCaller(ctx); !got.Anonymous() {
		t.Fatalf("no principal: %+v, want the anonymous caller", got)
	}
	if got := GetCaller(ContextWithPrincipalID(ctx, "u1")); got.Subject != "u1" || got.Kind != access.KindUser || got.Grants != nil {
		t.Fatalf("a principal ID alone: %+v, want the user u1", got)
	}
	party := Caller{Subject: "p1", Kind: access.KindParty, Grants: []access.GrantReach{{ID: "g1", TargetType: "Contract", TargetID: "c1"}}}
	withParty := ContextWithCaller(ctx, party)
	if got := GetCaller(withParty); got.Kind != access.KindParty || len(got.Grants) != 1 || GetPrincipalID(withParty) != "p1" {
		t.Fatalf("a whole principal: %+v", got)
	}
	// An auth step that names another principal afterwards wins.
	if got := GetCaller(ContextWithPrincipalID(withParty, "u2")); got.Subject != "u2" || got.Kind != access.KindUser {
		t.Fatalf("a later principal ID: %+v, want the user u2", got)
	}
}

type authority struct {
	tokens map[string]access.GrantAuthentication
}

func (a authority) AuthenticateGrant(_ context.Context, token string, _ time.Time) (access.GrantAuthentication, error) {
	if found, ok := a.tokens[token]; ok {
		return found, nil
	}
	if token == "grant_ended" {
		return access.GrantAuthentication{}, fmt.Errorf("The grant has ended: %w", access.ErrGrantRefused)
	}
	return access.GrantAuthentication{}, fmt.Errorf("Unknown grant: %w", access.ErrGrantRefused)
}

func TestGrantAuthenticatorRunsATokenAsItsHolderAndLeavesTheRestToInner(t *testing.T) {
	holder := access.Principal{Subject: "p1", Kind: access.KindParty, Grants: []access.GrantReach{{ID: "g1"}}, Credential: &access.Credential{Kind: access.CredentialGrant, ID: "g1"}}
	auth := GrantAuthenticator(authority{tokens: map[string]access.GrantAuthentication{
		"grant_good": {Principal: holder, Role: "tenant", Permissions: []string{"requests.read"}},
		"grant_run":  {Principal: access.Principal{Subject: "r1", Kind: access.KindAgent}, Role: "worker-agent", Permissions: []string{"requests.read"}},
	}}, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token, ok := BearerToken(r.Header); ok {
				r = r.WithContext(ContextWithPrincipalID(r.Context(), token))
			}
			next.ServeHTTP(w, r)
		})
	}, nil)
	var seen Caller
	var roles []Role
	handler := auth(RequirePermissions("requests.read")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, roles = GetCaller(r.Context()), GetRoles(r.Context())
	})))
	call := func(token string) int {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if code := call("grant_good"); code != http.StatusOK || seen.Subject != "p1" || seen.Kind != access.KindParty || len(roles) != 1 || roles[0].Permissions[0] != "requests.read" {
		t.Fatalf("a good grant: %d, %+v, %+v", code, seen, roles)
	}
	// An agent run's token is the dispatcher's to resolve, narrowed to
	// whom the run acts for and held to its autonomy (D192): not as its
	// grant's holder.
	for _, token := range []string{"grant_ended", "grant_unknown", "grant_run"} {
		if code := call(token); code != http.StatusUnauthorized {
			t.Fatalf("%s: %d, want 401", token, code)
		}
	}
	// inner's principal holds no permission: 403, not a grant's 401.
	if code := call("u1"); code != http.StatusForbidden {
		t.Fatalf("a user's token: %d, want 403", code)
	}
	if code := call(""); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d, want 401", code)
	}
}
