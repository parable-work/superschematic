// Package childenv is the environment of a child process that runs a
// project's code: the sidecar documents' harness, the install of the
// output root's Bun workspace, a site's build and a bundle's (D47,
// amended). Such a process gets an allowlisted environment, cut down from
// the one superschematic runs with, so a dependency's install script, a
// build plugin or a sidecar document finds none of the credentials a stack
// command holds in its environment: the Google Cloud credentials file's
// path and the gcloud override, a Cloudflare token, the state's
// passphrase, or the variables with which GitHub Actions mints an
// identity token.
//
// An allowlist, not a list of credentials: an install script reads
// whatever a shell exports, and the next credential has a name no list
// knows yet.
//
// The child's home is a scratch directory of its own, not the user's, so
// a script that looks for credentials where tools keep them under the
// home (gcloud's, Pulumi's, wrangler's, npm's, Docker's, AWS's, SSH's)
// finds an empty directory. The caches stay the user's, which Bun's
// installs need to stay fast. This is a mitigation, not a boundary: a
// child runs as the same user and can still open a file by a path it
// guesses. Only running the project's code where no credential is, `stack
// prepare` before signing in, is a boundary.
package childenv

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// PassVariable names the variable that lists, separated by commas or
// spaces, more variables a child receives: a private registry's token,
// say. It never passes a variable Denied refuses. Naming HOME in it keeps
// the user's home, and the configuration directories under it, for the
// child: what a private registry configured only in the user's ~/.npmrc
// needs.
const PassVariable = "SUPERSCHEMATIC_CHILD_ENV"

// allowed are the variables every child receives when they are set: what
// a shell, a package manager and a bundler need to find their tools,
// caches and network. On Windows a name matches in any case.
var allowed = []string{
	// The process's place on the machine.
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "PWD",
	"TMPDIR", "TMP", "TEMP",
	// Locale and terminal.
	"LANG", "LANGUAGE", "TZ", "TERM", "COLORTERM", "NO_COLOR", "FORCE_COLOR", "CI",
	// The network: proxies and certificate authorities.
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
	"http_proxy", "https_proxy", "no_proxy", "all_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
	// Directories tools keep their configuration and caches in.
	"XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR",
	// Bun's and Node's own.
	"BUN_INSTALL", "BUN_INSTALL_CACHE_DIR", "BUN_RUNTIME_TRANSPILER_CACHE_PATH", "NODE_ENV",
	// Windows' system variables, which a process there cannot start
	// without.
	"SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT", "SYSTEMDRIVE",
	"USERPROFILE", "APPDATA", "LOCALAPPDATA", "PROGRAMDATA", "HOMEDRIVE", "HOMEPATH",
	"PROGRAMFILES", "PROGRAMFILES(X86)", "COMMONPROGRAMFILES", "NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE",
}

// allowedPrefixes are the prefixes of variables every child receives: the
// locale's categories.
var allowedPrefixes = []string{"LC_"}

// The credentials no child receives, even when PassVariable lists them:
// names with deniedPrefixes, names in denied, names ending in
// deniedSuffixes, and superschematic's own ending in
// deniedSuperschematicSuffixes.
var (
	deniedPrefixes = []string{
		"GOOGLE_", "CLOUDSDK_", "GCLOUD_", "GCP_",
		"CLOUDFLARE_", "CF_", "WRANGLER_",
		"PULUMI_", "AWS_", "AZURE_", "ARM_", "ACTIONS_",
	}
	denied = []string{
		"GITHUB_TOKEN", "GH_TOKEN", "SSH_AUTH_SOCK", "DOCKER_AUTH_CONFIG", "KUBECONFIG", "VAULT_TOKEN",
	}
	deniedSuffixes               = []string{"_PASSPHRASE"}
	deniedSuperschematicSuffixes = []string{"_TOKEN", "_SECRET", "_KEY"}
)

// Denied reports whether name is a credential's, which no child receives.
func Denied(name string) bool {
	upper := strings.ToUpper(name)
	if slices.Contains(denied, upper) {
		return true
	}
	for _, s := range deniedSuffixes {
		if strings.HasSuffix(upper, s) {
			return true
		}
	}
	if strings.HasPrefix(upper, "SUPERSCHEMATIC_") {
		for _, s := range deniedSuperschematicSuffixes {
			if strings.HasSuffix(upper, s) {
				return true
			}
		}
	}
	for _, p := range deniedPrefixes {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	return false
}

// Environ returns the environment of a child that runs project code: this
// process's, cut down by Filter, with a scratch home made for it
// (WithHome), and a function that removes the scratch home once the child
// ended. When PassVariable names HOME, the child keeps the user's.
func Environ() ([]string, func(), error) {
	env := Filter(os.Environ())
	if passes(os.Environ(), "HOME") {
		return env, func() {}, nil
	}
	home, err := os.MkdirTemp("", "superschematic-child-home-")
	if err != nil {
		return nil, nil, err
	}
	realHome, _ := os.UserHomeDir()
	return WithHome(env, home, realHome), func() { _ = os.RemoveAll(home) }, nil
}

// homeVariables point at the user's home or at the configuration and data
// under it, which a child's scratch home replaces.
var homeVariables = []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "APPDATA", "LOCALAPPDATA", "HOMEDRIVE", "HOMEPATH"}

// WithHome returns env, a child's, with home in place of the user's home,
// realHome: HOME, Windows' twins and the configuration and data
// directories under it point into home, while the caches stay the user's,
// XDG_CACHE_HOME and Bun's install cache, which an install reads and
// writes.
func WithHome(env []string, home, realHome string) []string {
	get := func(name string) string {
		for _, kv := range env {
			if n, v, ok := strings.Cut(kv, "="); ok && same(n, name) {
				return v
			}
		}
		return ""
	}
	cache, bunCache := get("XDG_CACHE_HOME"), get("BUN_INSTALL_CACHE_DIR")
	if realHome != "" {
		if cache == "" {
			cache = filepath.Join(realHome, ".cache")
		}
		if bunCache == "" {
			bun := get("BUN_INSTALL")
			if bun == "" {
				bun = filepath.Join(realHome, ".bun")
			}
			bunCache = filepath.Join(bun, "install", "cache")
		}
	}
	replaced := append(slices.Clone(homeVariables), "XDG_CACHE_HOME", "BUN_INSTALL_CACHE_DIR")
	out := slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		n, _, _ := strings.Cut(kv, "=")
		return slices.ContainsFunc(replaced, func(h string) bool { return same(n, h) })
	})
	out = append(out,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
	)
	if runtime.GOOS == "windows" {
		out = append(out,
			"USERPROFILE="+home,
			"APPDATA="+filepath.Join(home, "AppData", "Roaming"),
			"LOCALAPPDATA="+filepath.Join(home, "AppData", "Local"),
		)
	}
	if cache != "" {
		out = append(out, "XDG_CACHE_HOME="+cache)
	}
	if bunCache != "" {
		out = append(out, "BUN_INSTALL_CACHE_DIR="+bunCache)
	}
	return out
}

// Filter returns the variables of env, each `NAME=value`, that a child
// receives: the allowed ones, and the ones env's PassVariable lists,
// except a credential's. The order is env's.
func Filter(env []string) []string {
	extra := passed(env)
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" || Denied(name) {
			continue
		}
		if slices.ContainsFunc(allowed, func(a string) bool { return same(a, name) }) ||
			slices.ContainsFunc(extra, func(e string) bool { return same(e, name) }) ||
			hasPrefix(name, allowedPrefixes) {
			out = append(out, kv)
		}
	}
	return out
}

// passed returns the names env's PassVariable lists.
func passed(env []string) []string {
	var out []string
	for _, kv := range env {
		if name, value, ok := strings.Cut(kv, "="); ok && same(name, PassVariable) {
			out = append(out, strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' })...)
		}
	}
	return out
}

// passes reports whether env's PassVariable lists name.
func passes(env []string, name string) bool {
	return slices.ContainsFunc(passed(env), func(p string) bool { return same(p, name) })
}

// same reports whether two variables' names are one: on Windows, whose
// variables' names are case-insensitive (`Path` is PATH), in any case.
func same(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func hasPrefix(name string, prefixes []string) bool {
	if runtime.GOOS == "windows" {
		name = strings.ToUpper(name)
	}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
