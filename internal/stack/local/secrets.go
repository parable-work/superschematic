package local

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
)

// StateRoot is the directory, under the schemas root, where the CLI keeps
// what belongs to one machine and never to the repository. It holds a
// .gitignore that ignores everything in it, so no secret in it is
// committed by accident.
const StateRoot = ".superschematic"

// SecretsFile is the file, in a local environment's state directory, that
// holds the environment's secret values (docs/stack-model.md, section
// 4.2): a line `<Type>.<FIELD>=<value>` per secret, the ID of
// ir.StackSecret, with a value that is not plain written as a Go quoted
// string. Nothing writes it into the output root.
const SecretsFile = "secrets.env"

// StateDir returns the state directory of a local environment:
// `<schemas-root>/.superschematic/local/<stack>/<environment>`. The local
// provisioner's state backend is this directory (StateBackend), and it
// reads the environment's secrets from SecretsFile in it.
func StateDir(schemasRoot, stack, environment string) string {
	return filepath.Join(schemasRoot, StateRoot, Target, stack, environment)
}

// EnsureStateDir creates a local environment's state directory, readable
// by its owner alone, and the .gitignore that keeps StateRoot out of the
// repository, and returns the directory.
func EnsureStateDir(schemasRoot, stack, environment string) (string, error) {
	dir := StateDir(schemasRoot, stack, environment)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("local: state directory: %w", err)
	}
	ignore := filepath.Join(schemasRoot, StateRoot, ".gitignore")
	if _, err := os.Stat(ignore); errors.Is(err, os.ErrNotExist) {
		content := "# Written by superschematic: what is here belongs to this machine, secrets included.\n*\n"
		if err := os.WriteFile(ignore, []byte(content), 0o644); err != nil {
			return "", fmt.Errorf("local: state directory: %w", err)
		}
	}
	return dir, nil
}

// StateBackend returns the state backend of a local environment whose state
// directory is dir.
func StateBackend(dir string) registry.StateBackend {
	return registry.StateBackend{URL: (&url.URL{Scheme: "file", Path: filepath.ToSlash(dir)}).String()}
}

// stateDirOf returns the directory a file:// state backend names.
func stateDirOf(backend registry.StateBackend) (string, error) {
	if backend.URL == "" {
		return "", errors.New("the request names no state backend, the directory that holds the environment's secrets file")
	}
	u, err := url.Parse(backend.URL)
	if err != nil || u.Scheme != "file" || u.Path == "" {
		return "", fmt.Errorf("state backend %q is not a file:// directory", backend.URL)
	}
	return filepath.FromSlash(u.Path), nil
}

// secretIDPattern is the shape of a secret's ID: the declaring type and
// the field.
var secretIDPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\.[A-Za-z_][A-Za-z0-9_]*$`)

// ReadSecrets reads a secrets file. A file that does not exist holds no
// secret.
func ReadSecrets(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("local: secrets: %w", err)
	}
	secrets := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		id, value, ok := strings.Cut(text, "=")
		id = strings.TrimSpace(id)
		if !ok || !secretIDPattern.MatchString(id) {
			return nil, fmt.Errorf("local: %s:%d: want <Type>.<FIELD>=<value>", path, line)
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, `"`) {
			unquoted, err := strconv.Unquote(value)
			if err != nil {
				return nil, fmt.Errorf("local: %s:%d: %s: a quoted value is a Go quoted string: %w", path, line, id, err)
			}
			value = unquoted
		}
		if _, dup := secrets[id]; dup {
			return nil, fmt.Errorf("local: %s:%d: %s is set twice", path, line, id)
		}
		secrets[id] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("local: %s: %w", path, err)
	}
	return secrets, nil
}

// WriteSecret sets one secret in a secrets file, keeping the others, and
// writes the file readable by its owner alone.
func WriteSecret(path, id, value string) error {
	if !secretIDPattern.MatchString(id) {
		return fmt.Errorf("local: secret ID %q is not <Type>.<FIELD>", id)
	}
	secrets, err := ReadSecrets(path)
	if err != nil {
		return err
	}
	secrets[id] = value
	ids := make([]string, 0, len(secrets))
	for key := range secrets {
		ids = append(ids, key)
	}
	sort.Strings(ids)
	var b strings.Builder
	b.WriteString("# The secret values of a local environment, written by superschematic. Never commit this file.\n")
	for _, key := range ids {
		v := secrets[key]
		if v != strings.TrimSpace(v) || strings.ContainsAny(v, "\"\n\r\t\\#") {
			v = strconv.Quote(v)
		}
		fmt.Fprintf(&b, "%s=%s\n", key, v)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("local: secrets: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".secrets-*")
	if err != nil {
		return fmt.Errorf("local: secrets: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(b.String()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("local: secrets: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("local: secrets: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("local: secrets: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("local: secrets: %w", err)
	}
	return nil
}
