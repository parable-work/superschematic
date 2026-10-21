package projectengine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// LinkFile is the file a generated Go or TypeScript entrypoint holds when
// the engine it opens is built from the project's engine crate (D58): which
// crate, and which member of it the entrypoint takes, the ffi member's
// static archive a Go binary links, or the napi member's addon a
// TypeScript process loads. The local target reads it to build that member
// before it builds or runs the entrypoint, as a checkout's Dockerfile
// builds it in its engine stage. A Rust entrypoint depends on the crate
// itself and has none.
const LinkFile = "engine-project.json"

// AddonEnv names the variable that names the Node-API addon file every
// engine a process opens loads, in place of the core's addon.
const AddonEnv = "SUPERSCHEMATIC_ENGINE_ADDON"

// Link is what LinkFile holds. Its paths are relative to the entrypoint's
// directory and slash-separated, so the file is the same on every machine
// that builds the output root where it lies.
type Link struct {
	// Database is the engine database whose crate it is.
	Database string `json:"database"`

	// Crate is the project's engine crate, the workspace's root.
	Crate string `json:"crate"`

	// Member is the member the entrypoint takes: FFIDir for a Go
	// entrypoint, NapiDir for a TypeScript one.
	Member string `json:"member"`

	// Lockfile is the lock file of the engine's Cargo workspace, which a
	// crate with none of its own starts from, as the image's engine stage
	// does, so the crate builds the versions the engine's are tested with.
	// Empty when no [paths] key names the engine's checkout.
	Lockfile string `json:"lockfile,omitempty"`
}

// WriteLink writes l into dir's LinkFile.
func WriteLink(dir string, l Link) error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("projectengine: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, LinkFile), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("projectengine: %w", err)
	}
	return nil
}

// ReadLink reads dir's LinkFile: nil when dir has none. It refuses a member
// that is not one of the crate's bindings and a path that is not relative.
func ReadLink(dir string) (*Link, error) {
	file := filepath.Join(dir, LinkFile)
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var l Link
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	switch {
	case l.Member != FFIDir && l.Member != NapiDir:
		return nil, fmt.Errorf("%s: member %q is none of the crate's bindings (%s, %s)", file, l.Member, FFIDir, NapiDir)
	case l.Crate == "" || path.IsAbs(l.Crate) || strings.Contains(l.Crate, `\`):
		return nil, fmt.Errorf("%s: crate %q is not a slash-separated path relative to %s", file, l.Crate, dir)
	case l.Lockfile != "" && (path.IsAbs(l.Lockfile) || strings.Contains(l.Lockfile, `\`)):
		return nil, fmt.Errorf("%s: lockfile %q is not a slash-separated path relative to %s", file, l.Lockfile, dir)
	}
	return &l, nil
}

// LinkOf is the Link of an entrypoint at dir to member of the project's
// engine crate of database at crate, with the engine's workspace lock file
// at lockfile when one is known, each path relative to dir.
func LinkOf(dir, database, crate, member, lockfile string) (Link, error) {
	l := Link{Database: database, Member: member}
	rel, err := relSlash(dir, crate)
	if err != nil {
		return Link{}, fmt.Errorf("projectengine: the engine crate of %s: %w", database, err)
	}
	l.Crate = rel
	if lockfile != "" {
		if l.Lockfile, err = relSlash(dir, lockfile); err != nil {
			return Link{}, fmt.Errorf("projectengine: the engine's lock file: %w", err)
		}
	}
	return l, nil
}

// relSlash is target relative to dir, slash-separated.
func relSlash(dir, target string) (string, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absDir, absTarget)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}
