package stack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	ir "github.com/parable-work/superschematic/ir"
)

// EnvironmentFile is the name of the resolved environment's file.
const EnvironmentFile = "environment.json"

// EnvironmentPath returns where a resolved environment is written:
// `<outputRoot>/stack/<stack>/<environment>/environment.json`.
func EnvironmentPath(outputRoot, stack, environment string) string {
	return filepath.Join(outputRoot, "stack", stack, environment, EnvironmentFile)
}

// Marshal encodes a resolved environment in its stable JSON form: two-space
// indentation, fields in the order the IR declares them, map keys sorted,
// no HTML escaping, and a final newline.
func Marshal(env *ir.ResolvedEnvironment) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(env); err != nil {
		return nil, fmt.Errorf("stack: encode environment %s: %w", env.Environment, err)
	}
	return buf.Bytes(), nil
}

// Unmarshal decodes an environment.json, references included.
func Unmarshal(data []byte) (*ir.ResolvedEnvironment, error) {
	var env ir.ResolvedEnvironment
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("stack: decode environment: %w", err)
	}
	if env.Version != ir.ResolvedEnvironmentVersion {
		return nil, fmt.Errorf("stack: environment %s has format version %d; this build reads version %d", env.Environment, env.Version, ir.ResolvedEnvironmentVersion)
	}
	return &env, nil
}

// Write writes a resolved environment under outputRoot and returns the
// file's path.
func Write(outputRoot string, env *ir.ResolvedEnvironment) (string, error) {
	data, err := Marshal(env)
	if err != nil {
		return "", err
	}
	path := EnvironmentPath(outputRoot, env.Stack, env.Environment)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("stack: write environment %s: %w", env.Environment, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("stack: write environment %s: %w", env.Environment, err)
	}
	return path, nil
}
