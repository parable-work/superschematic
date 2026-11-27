package notify

import (
	"context"
	"strings"
)

// ResetIdentity is what a password reset reads a login through and writes
// the new password with: the identity service (identity.Service), whose
// UserOfLogin finds a login's user and whose ResetPassword ends every
// session of the user.
type ResetIdentity interface {
	UserOfLogin(ctx context.Context, login string) (string, bool, error)
	ResetPassword(ctx context.Context, userID, password string) error
}

// PasswordReset is password reset over a channel (D81, D153): Request
// sends a code to the email address the login names, through the email
// adapter, held on the user's email channel identity; Complete checks it
// and writes the new password, which ends every session of the user, and
// marks the address verified, since its owner proved they read it.
// Request answers alike for a login that does not exist, so it tells no
// one which do.
type PasswordReset struct {
	Notifier *Notifier
	Identity ResetIdentity
}

// identityOf is the email identity of the user login names, made
// unverified when the user has none; false for a login of no user, or no
// email address.
func (p PasswordReset) identityOf(ctx context.Context, login string) (row, bool, error) {
	n := p.Notifier
	t, err := n.identitiesTable()
	if err != nil {
		return nil, false, err
	}
	user, ok, err := p.Identity.UserOfLogin(ctx, login)
	if err != nil || !ok {
		return nil, false, err
	}
	address := NormaliseAddress("email", login)
	existing, err := n.findOne(ctx, n.host, t.where().eq("kind", "email").eq("address", address).eq("principal", user).null("revokedAt"), "")
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		return existing, true, nil
	}
	if !strings.Contains(address, "@") {
		return nil, false, nil
	}
	created, err := n.insert(ctx, n.host, t, map[string]any{"kind": "email", "address": address, "principalKind": "user", "principal": user, "trust": "unverified"})
	return created, err == nil, err
}

// Request sends a reset code to the login's address.
func (p PasswordReset) Request(ctx context.Context, login string) error {
	identity, ok, err := p.identityOf(ctx, login)
	if err != nil || !ok {
		return err
	}
	return p.Notifier.StartVerification(ctx, identity.str("id"), "reset")
}

// Complete checks code for login and sets password, and reports whether
// it did.
func (p PasswordReset) Complete(ctx context.Context, login, code, password string) (bool, error) {
	n := p.Notifier
	identity, ok, err := p.identityOf(ctx, login)
	if err != nil || !ok {
		return false, err
	}
	t, err := n.identitiesTable()
	if err != nil {
		return false, err
	}
	expires := identity.time("codeExpiresAt")
	if identity.str("codePurpose") != "reset" || identity.str("codeHash") == "" || expires == nil || expires.Before(n.now()) {
		return false, nil
	}
	id := identity.str("id")
	if identity.str("codeHash") != CodeHash(code) {
		attempts := identity.int("codeAttempts") + 1
		values := map[string]any{"codeAttempts": attempts}
		if attempts >= CodeAttempts {
			values = map[string]any{"codeHash": nil, "codePurpose": nil, "codeExpiresAt": nil, "codeAttempts": attempts}
		}
		return false, n.update(ctx, n.host, t, id, values)
	}
	if err := p.Identity.ResetPassword(ctx, identity.str("principal"), password); err != nil {
		return false, err
	}
	return true, n.update(ctx, n.host, t, id, map[string]any{"codeHash": nil, "codePurpose": nil, "codeExpiresAt": nil, "codeAttempts": 0, "trust": "verified", "verifiedAt": n.now()})
}
