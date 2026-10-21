package cloudflare

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// GitHub is what bootstrap's --github calls to store the generated CI's
// secrets in the repository the CI runs in (section 18.3, D64). The
// default runs the gh CLI, signed in as the person; tests pass a fake.
type GitHub interface {
	// Ready returns nil when it can set secrets: on the default, gh is on
	// PATH and signed in. Else its error says what is missing, and
	// bootstrap prints the commands to run instead.
	Ready(ctx context.Context) error

	// EnsureEnvironment creates a deployment environment of the
	// repository (`acme/shop`), unless it exists, whose secrets a deploy
	// job reads.
	EnsureEnvironment(ctx context.Context, repository, environment string) (created bool, err error)

	// SetSecret sets an Actions secret of the repository, or of one of its
	// environments when environment is not empty. The value never reaches
	// a command line.
	SetSecret(ctx context.Context, repository, environment, name string, value []byte) error
}

// github returns the extension's GitHub, or the gh CLI's.
func (e Extension) github() GitHub {
	if e.GitHub != nil {
		return e.GitHub
	}
	return ghCLI{}
}

// ghCLI is GitHub over the gh CLI.
type ghCLI struct{}

// ghTimeout bounds one gh command.
const ghTimeout = 2 * time.Minute

// run runs gh with args, value on its standard input, and returns its
// standard output.
func (ghCLI) run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("gh %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func (g ghCLI) Ready(ctx context.Context) error {
	if _, err := exec.LookPath("gh"); err != nil {
		return errors.New("gh, the GitHub CLI, is not on PATH")
	}
	if _, err := g.run(ctx, nil, "auth", "status"); err != nil {
		return fmt.Errorf("gh is not signed in (gh auth login): %w", err)
	}
	return nil
}

func (g ghCLI) EnsureEnvironment(ctx context.Context, repository, environment string) (bool, error) {
	path := "repos/" + repository + "/environments/" + environment
	_, err := g.run(ctx, nil, "api", path, "--silent")
	if err == nil {
		return false, nil
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		return false, err
	}
	if _, err := g.run(ctx, nil, "api", "--method", "PUT", path, "--silent"); err != nil {
		return false, err
	}
	return true, nil
}

func (g ghCLI) SetSecret(ctx context.Context, repository, environment, name string, value []byte) error {
	_, err := g.run(ctx, value, setSecretArgs(repository, environment, name)...)
	return err
}

// setSecretArgs are gh's arguments that set a secret, whose value gh reads
// from its standard input.
func setSecretArgs(repository, environment, name string) []string {
	args := []string{"secret", "set", name, "--repo", repository}
	if environment != "" {
		args = append(args, "--env", environment)
	}
	return args
}
