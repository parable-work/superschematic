package schemadeps

import (
	"regexp"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// schemadeps scans manifests superschematic already wrote and pins consumer go.mod /
// tsconfig files; it has no generator.Options to thread naming through, so
// every name it matches comes from the process-wide value the CLI set
// (naming.Active). The regexes are rebuilt per call because the active
// naming can change between tests.

func goModulePrefix() string {
	return naming.Active().GoModulePrefix()
}

// goRequireRE matches require lines for modules under the Go module root.
func goRequireRE() *regexp.Regexp {
	return regexp.MustCompile(`(?m)^\s*((?:` + regexp.QuoteMeta(goModulePrefix()) + `[^\s]+))\s+v`)
}

// pyDepNameRE matches pyproject dependency entries for generated Python
// types or SDK modules.
func pyDepNameRE() *regexp.Regexp {
	n := naming.Active()
	types := regexp.QuoteMeta(n.PythonTypesModulePrefix) + `[a-z0-9_]+`
	sdk := regexp.QuoteMeta(n.PythonSDKModulePrefix) + `[a-z0-9_]+` + regexp.QuoteMeta(n.PythonSDKModuleSuffix)
	return regexp.MustCompile(`(?m)^\s*"?(` + types + `|` + sdk + `)"?(?:>=|==|~=|<|>|!)`)
}

// npmScopeRE captures the package name under the npm scope.
func npmScopeRE() *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(naming.Active().NpmServicePackagePrefix()) + `(.+)$`)
}

// pyTypesStem returns the schema stem of a generated Python types module
// name, reporting whether name is one.
func pyTypesStem(name string) (string, bool) {
	prefix := naming.Active().PythonTypesModulePrefix
	if !strings.HasPrefix(name, prefix) {
		return "", false
	}
	return strings.ReplaceAll(strings.TrimPrefix(name, prefix), "_", "-"), true
}

// pySDKStem returns the schema stem of a generated Python SDK module name,
// reporting whether name is one.
func pySDKStem(name string) (string, bool) {
	n := naming.Active()
	if !strings.HasPrefix(name, n.PythonSDKModulePrefix) || !strings.HasSuffix(name, n.PythonSDKModuleSuffix) {
		return "", false
	}
	stem := strings.TrimSuffix(strings.TrimPrefix(name, n.PythonSDKModulePrefix), n.PythonSDKModuleSuffix)
	return strings.ReplaceAll(stem, "_", "-"), true
}

// rustCrateStem strips the Rust crate prefix and the given suffix, reporting
// whether name had both.
func rustCrateStem(name, suffix string) (string, bool) {
	prefix := naming.Active().RustCratePrefix
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix), true
}
