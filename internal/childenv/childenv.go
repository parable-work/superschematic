// Package childenv is the environment of a child process that runs a
// project's code: the sidecar documents' harness, the install of the
// output root's Bun workspace, a site's build and a bundle's (D47,
// amended). Such a process gets an allowlisted environment, cut down from
// the one superschematic runs with, so a dependency's install script, a
// build plugin or a sidecar document cannot read the credentials a stack
// command holds: the Google Cloud credentials file's path and the gcloud
// override, a Cloudflare token, the state's passphrase, or the variables
// with which GitHub Actions mints an identity token.
//
// An allowlist, not a list of credentials: an install script reads
// whatever a shell exports, and the next credential has a name no list
// knows yet.
package childenv

import (
	"os"
	"slices"
	"strings"
)

// PassVariable names the variable that lists, separated by commas or
// spaces, more variables a child receives: a private registry's token,
// say. It never passes a variable Denied refuses.
const PassVariable = "SUPERSCHEMATIC_CHILD_ENV"

// allowed are the variables every child receives when they are set: what
// a shell, a package manager and a bundler need to find their tools,
// caches and network.
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
	"SYSTEMROOT", "SystemRoot", "WINDIR", "COMSPEC", "PATHEXT", "SYSTEMDRIVE",
	"USERPROFILE", "APPDATA", "LOCALAPPDATA", "PROGRAMDATA", "HOMEDRIVE", "HOMEPATH",
}

// allowedPrefixes are the prefixes of variables every child receives: the
// locale's categories.
var allowedPrefixes = []string{"LC_"}

// deniedPrefixes and denied name the credentials no child receives, even
// when PassVariable lists them.
var (
	deniedPrefixes = []string{"GOOGLE_", "CLOUDSDK_", "CLOUDFLARE_", "PULUMI_", "AWS_", "AZURE_", "ARM_", "ACTIONS_"}
	denied         = []string{"GITHUB_TOKEN", "GH_TOKEN", "SUPERSCHEMATIC_BOOTSTRAP_PASSPHRASE"}
)

// Denied reports whether name is a credential's, which no child receives.
func Denied(name string) bool {
	upper := strings.ToUpper(name)
	if slices.Contains(denied, upper) || strings.HasSuffix(upper, "_PASSPHRASE") {
		return true
	}
	for _, p := range deniedPrefixes {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	return false
}

// Environ returns the environment of a child that runs project code: this
// process's, cut down by Filter.
func Environ() []string { return Filter(os.Environ()) }

// Filter returns the variables of env, each `NAME=value`, that a child
// receives: the allowed ones, and the ones env's PassVariable lists,
// except a credential's. The order is env's.
func Filter(env []string) []string {
	extra := map[string]bool{}
	for _, kv := range env {
		if name, value, ok := strings.Cut(kv, "="); ok && name == PassVariable {
			for _, n := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' }) {
				extra[n] = true
			}
		}
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" || Denied(name) {
			continue
		}
		if slices.Contains(allowed, name) || extra[name] || hasPrefix(name, allowedPrefixes) {
			out = append(out, kv)
		}
	}
	return out
}

func hasPrefix(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
