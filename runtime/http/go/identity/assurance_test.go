package identity_test

import (
	"context"
	"testing"
	"time"

	"github.com/parable-work/superschematic/runtime/http/go/identity"
	"github.com/parable-work/superschematic/runtime/http/go/session"
)

// A session's principal reports when the session signed in (D200), not
// when it was last used, and WithPrincipal puts it on the context as the
// call's assurance, which a generated server hands the behaviors' core
// and an Approvals requirement's recent-sign-in proof reads. An API key's
// principal signed no one in and reports none.
func TestASessionsPrincipalReportsWhenItSignedIn(t *testing.T) {
	eachHarness(t, testConfig, func(t *testing.T, h *harness) {
		signedIn := h.clock.Now()
		token := h.login("member@example.com", userPassword)
		h.clock.Advance(10 * time.Minute)
		p, err := h.svc.AuthenticateToken(context.Background(), token, identity.TransportBearer)
		if err != nil {
			t.Fatal(err)
		}
		if p.SignedInAt == nil || !p.SignedInAt.Equal(signedIn) {
			t.Fatalf("the session's principal signed in at %v, want %v", p.SignedInAt, signedIn)
		}
		ctx := identity.WithPrincipal(context.Background(), p)
		assurance, ok := session.AssuranceOf(ctx)
		if !ok || assurance.SignedInAt == nil || !assurance.SignedInAt.Equal(signedIn) || assurance.UserVerified {
			t.Fatalf("the context's assurance = %+v, %v", assurance, ok)
		}

		key, err := h.svc.CreateAPIKey(context.Background(), p, identity.CreateAPIKeyInput{Name: "ci"})
		if err != nil {
			t.Fatal(err)
		}
		byKey, err := h.svc.AuthenticateToken(context.Background(), key.Token, identity.TransportBearer)
		if err != nil {
			t.Fatal(err)
		}
		if byKey.SignedInAt != nil {
			t.Fatalf("an API key's principal signed in at %v", byKey.SignedInAt)
		}
		if _, ok := session.AssuranceOf(identity.WithPrincipal(context.Background(), byKey)); ok {
			t.Fatal("an API key's principal put an assurance on the context")
		}
	})
}
