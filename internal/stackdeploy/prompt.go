package stackdeploy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// Prompter asks a person for a value. The CLI's reads the terminal with
// its echo off; tests pass a fake terminal.
type Prompter interface {
	// Secret shows prompt and returns the line typed, without its line
	// ending and without echoing it.
	Secret(prompt string) ([]byte, error)
}

// secretPrompt is the prompt for an application secret: its identity and
// the servers that read it.
func secretPrompt(secret *ir.StackSecret) string {
	return fmt.Sprintf("Value of %s (read by %s): ", secret.ID, strings.Join(secret.Readers, ", "))
}

// credentialPrompt is the prompt for a platform credential.
func credentialPrompt(c Credential) string {
	return fmt.Sprintf("%s (%s): ", c.Description, c.Secret)
}

// promptSecret asks for one value and stores it under id. An empty value
// is refused, and the value never reaches the log.
func promptSecret(ctx context.Context, s *session, p Prompter, id, prompt string) error {
	value, err := p.Secret(prompt)
	if err != nil {
		return fmt.Errorf("read the value of %s: %w", id, err)
	}
	if len(value) == 0 {
		return fmt.Errorf("no value entered for %s; nothing was stored", id)
	}
	if err := s.target.Secrets.Set(ctx, s.env, id, value); err != nil {
		if errors.Is(err, registry.ErrSecretNotCreated) {
			return fmt.Errorf("%s has nowhere to go yet: `stack deploy %s` creates an application secret's storage in its infrastructure step, "+
				"and `stack bootstrap %s` a credential's (%w)", id, s.env.Environment, s.env.Environment, err)
		}
		return fmt.Errorf("store %s: %w", id, err)
	}
	s.logf("stored a value of %s", id)
	return nil
}
