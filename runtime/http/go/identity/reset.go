package identity

import (
	"context"
	"errors"
)

// A password reset over a channel (D81, D153, D190): the notifier sends a
// code to the address a login names and checks the code replied with;
// what it asks of the service is the login's user and the new password,
// which ends every session of the user. The TypeScript service's
// resetPassword, and the Rust one's reset_password (D191).

// UserOfLogin is the id of the user whose login is login, and false for a
// login of no user.
func (s *Service) UserOfLogin(ctx context.Context, login string) (string, bool, error) {
	rec, err := s.store.FindLogin(ctx, login)
	var invalid *InvalidLoginError
	switch {
	case errors.Is(err, ErrNotFound) || errors.As(err, &invalid):
		return "", false, nil
	case err != nil:
		return "", false, err
	}
	return rec.User.ID, true, nil
}

// ResetPassword sets the user's password, which a reset's code checked,
// and revokes every session of theirs. The user's API keys live on.
func (s *Service) ResetPassword(ctx context.Context, userID, password string) error {
	var f fieldErrors
	checkPasswordField(&f, "password", password)
	if err := f.err(); err != nil {
		return err
	}
	hash, err := HashPassword(password, s.params)
	if err != nil {
		return err
	}
	return storeError(s.store.SetPassword(ctx, userID, hash, s.now(), ""), "User")
}
