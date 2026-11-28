package schemaconfig

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// A runtime provider an API's config lists (D159) is a directory or an
// npm name (#608). A directory starts with "." or "/", as Node.js tells a
// path from a package: "./providers/sms-relay", "../relays/sms". Anything
// else is a package's npm name, "@scope/sms-relay" or "sms-relay", which
// resolves from the service's node_modules as Node.js resolves it: the
// first node_modules/<name> in the service's directory or one above it.

// npmName is the form of a package name npm takes for a new package: an
// optional scope, and a name of lower-case URL-safe characters.
var npmName = regexp.MustCompile(`^(@[a-z0-9-~][a-z0-9-._~]*/)?[a-z0-9-~][a-z0-9-._~]*$`)

// maxNpmName is the longest package name npm takes.
const maxNpmName = 214

// IsProviderPackageName reports whether entry, one of a config's
// providers, names the package by its npm name rather than its directory:
// it starts with neither "." nor "/" (#608).
func IsProviderPackageName(entry string) bool {
	return entry != "" && !strings.HasPrefix(entry, ".") && !strings.HasPrefix(entry, "/")
}

// checkProviderEntry checks one entry of a config's providers: a clean
// relative directory in slash form that starts with "./" or "../", or a
// valid npm name.
func checkProviderEntry(entry string) error {
	if IsProviderPackageName(entry) {
		if len(entry) > maxNpmName || !npmName.MatchString(entry) {
			return fmt.Errorf("%q is no npm name; a provider is its package's npm name (@scope/name or name, lower-case) or its directory, which starts with \"./\" or \"../\" (#608)", entry)
		}
		return nil
	}
	clean := path.Clean(entry)
	switch {
	case path.IsAbs(entry):
		return fmt.Errorf("%q is an absolute path; a provider's directory is relative to the service's directory, so the config builds in any checkout", entry)
	case strings.Contains(entry, `\`):
		return fmt.Errorf("%q is no path in slash form", entry)
	case clean != entry && "./"+clean != entry:
		return fmt.Errorf("%q is no clean path; write %q", entry, cleanEntry(clean))
	}
	return nil
}

// cleanEntry is the clean form of a directory entry, which starts with
// "." so that it stays a directory.
func cleanEntry(clean string) string {
	if strings.HasPrefix(clean, ".") {
		return clean
	}
	return "./" + clean
}

// ResolveProvider returns the directory of the provider entry names, one
// of the providers the config of the service in serviceDir lists: the
// directory itself, relative to serviceDir, or the first
// node_modules/<entry> in serviceDir or a directory above it for an npm
// name (#608). A package that no node_modules holds is refused, naming
// every place looked in. The directory is absolute when serviceDir is.
func ResolveProvider(serviceDir, entry string) (string, error) {
	if !IsProviderPackageName(entry) {
		return filepath.Join(serviceDir, filepath.FromSlash(entry)), nil
	}
	start, err := filepath.Abs(serviceDir)
	if err != nil {
		return "", err
	}
	var looked []string
	for dir := start; ; {
		if filepath.Base(dir) != "node_modules" {
			candidate := filepath.Join(dir, "node_modules", filepath.FromSlash(entry))
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				return candidate, nil
			}
			looked = append(looked, filepath.Join(dir, "node_modules"))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("no package %s in the service's node_modules or one above it (looked in %s); install it, or list its directory, which starts with \"./\" or \"../\" (#608)", entry, strings.Join(looked, ", "))
}
