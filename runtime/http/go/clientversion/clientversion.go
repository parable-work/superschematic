// Package clientversion turns away a client too old for an API (D143).
//
// A generated SDK whose caller names its client sends
// `X-Client-Version: <name>/<version>` on every call. An API whose config
// says `clientVersions: true` reads MIN_CLIENT_VERSIONS, a list such as
// `cli=0.5.0,desktop=2.1.0`, and answers a request whose header names a
// listed client at a lower version with 426 and the problem
// `client_too_old`, whose details hold the client, its version and the
// minimum. Versions compare as semantic versions (semver.org 2.0.0). A
// request with no header, a client the list does not name, or a version
// that is no semantic version passes: a browser sends none.
//
// The TypeScript and Rust runtimes read the setting and decide each
// request alike (runtime/http/testdata/client_version_parity.json).
package clientversion

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/runtime/http/go/response"
)

const (
	// Header is the request header a client names itself in.
	Header = "X-Client-Version"
	// Setting is the environment variable that lists the minimums.
	Setting = "MIN_CLIENT_VERSIONS"
	// Code is the problem code of a client that is too old.
	Code = "client_too_old"
)

// semver is semver.org's own pattern of a semantic version 2.0.0.
var semver = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-((?:0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// clientName is a client's name: a letter or a digit, then up to 63 more
// of letters, digits, `.`, `_` and `-`.
var clientName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Version is a semantic version, parsed.
type Version struct {
	// core holds major, minor and patch, each as its digits.
	core [3]string
	// pre holds the pre-release identifiers, none for a release.
	pre []string
}

// ParseVersion reads a semantic version, as semver.org 2.0.0 writes one:
// three numbers without leading zeros, an optional pre-release after `-`
// and optional build metadata after `+`.
func ParseVersion(text string) (Version, bool) {
	m := semver.FindStringSubmatch(text)
	if m == nil {
		return Version{}, false
	}
	version := Version{core: [3]string{m[1], m[2], m[3]}}
	if m[4] != "" {
		version.pre = strings.Split(m[4], ".")
	}
	return version, true
}

// compareNumbers compares two numbers written as digits without leading
// zeros, of any length.
func compareNumbers(a, b string) int {
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func isNumber(identifier string) bool {
	for i := 0; i < len(identifier); i++ {
		if identifier[i] < '0' || identifier[i] > '9' {
			return false
		}
	}
	return true
}

// Compare orders two versions by semantic version precedence: -1 when v
// is older than other, 1 when it is newer and 0 when neither is. Build
// metadata takes no part.
func (v Version) Compare(other Version) int {
	for i := range v.core {
		if c := compareNumbers(v.core[i], other.core[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(v.pre) == 0 && len(other.pre) == 0:
		return 0
	case len(v.pre) == 0:
		return 1
	case len(other.pre) == 0:
		return -1
	}
	for i := 0; i < len(v.pre) && i < len(other.pre); i++ {
		a, b := v.pre[i], other.pre[i]
		aNumber, bNumber := isNumber(a), isNumber(b)
		var c int
		switch {
		case aNumber && bNumber:
			c = compareNumbers(a, b)
		case aNumber:
			c = -1
		case bNumber:
			c = 1
		default:
			c = strings.Compare(a, b)
		}
		if c != 0 {
			return c
		}
	}
	// Every shared identifier is equal: the longer list is the newer.
	switch {
	case len(v.pre) < len(other.pre):
		return -1
	case len(v.pre) > len(other.pre):
		return 1
	}
	return 0
}

// Compare orders two versions as text: -1, 0 or 1, and false when either
// is no semantic version.
func Compare(a, b string) (int, bool) {
	left, ok := ParseVersion(a)
	if !ok {
		return 0, false
	}
	right, ok := ParseVersion(b)
	if !ok {
		return 0, false
	}
	return left.Compare(right), true
}

// Minimums is the oldest version of each client an API serves. The zero
// value lists none, and turns no request away.
type Minimums struct {
	byClient map[string]minimum
}

type minimum struct {
	text    string
	version Version
}

// Parse reads MIN_CLIENT_VERSIONS: entries `<client>=<version>` separated
// by commas, with space allowed around each entry, name and version. An
// empty setting lists none. An entry without one `=`, a name that is not
// letters, digits, `.`, `_` and `-`, a minimum that is no semantic version
// and a client listed twice are errors: a server does not start on a
// minimum it cannot enforce.
func Parse(setting string) (Minimums, error) {
	minimums := Minimums{}
	for _, entry := range strings.Split(setting, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, "=")
		if len(parts) != 2 {
			return Minimums{}, fmt.Errorf("%s: %q is not <client>=<version>, as in cli=0.5.0,desktop=2.1.0", Setting, entry)
		}
		name, text := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if !clientName.MatchString(name) {
			return Minimums{}, fmt.Errorf("%s: %q is not a client's name: a letter or a digit, then letters, digits, '.', '_' and '-', 64 characters at most", Setting, name)
		}
		version, ok := ParseVersion(text)
		if !ok {
			return Minimums{}, fmt.Errorf("%s: the minimum of %s, %q, is not a semantic version such as 0.5.0", Setting, name, text)
		}
		if _, listed := minimums.byClient[name]; listed {
			return Minimums{}, fmt.Errorf("%s lists %s twice", Setting, name)
		}
		if minimums.byClient == nil {
			minimums.byClient = map[string]minimum{}
		}
		minimums.byClient[name] = minimum{text: text, version: version}
	}
	return minimums, nil
}

// Load reads the minimums from the environment variable
// MIN_CLIENT_VERSIONS, as a generated EnvConfig does: none when it is
// unset, and an error for a list no server starts with.
func Load() (Minimums, error) {
	return Parse(os.Getenv(Setting))
}

// Empty reports whether the list names no client.
func (m Minimums) Empty() bool { return len(m.byClient) == 0 }

// Listed returns each client's minimum as it was written, by client.
func (m Minimums) Listed() map[string]string {
	listed := make(map[string]string, len(m.byClient))
	for name, min := range m.byClient {
		listed[name] = min.text
	}
	return listed
}

// String writes the list as the setting holds it, clients in order.
func (m Minimums) String() string {
	names := make([]string, 0, len(m.byClient))
	for name := range m.byClient {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]string, len(names))
	for i, name := range names {
		entries[i] = name + "=" + m.byClient[name].text
	}
	return strings.Join(entries, ",")
}

// TooOld is what a client_too_old problem carries in details: the client,
// the version it sent and the oldest version the API serves.
type TooOld struct {
	Client  string `json:"client"`
	Version string `json:"version"`
	Minimum string `json:"minimum"`
}

// Detail is the problem's detail, for a person.
func (t TooOld) Detail() string {
	return fmt.Sprintf("%s %s is older than %s, the oldest version of %s this API serves. Upgrade %s.", t.Client, t.Version, t.Minimum, t.Client, t.Client)
}

// Check decides a request by its X-Client-Version header, `<name>/<version>`:
// the refusal of a listed client at a version under its minimum, and nil
// for every other request.
func (m Minimums) Check(header string) *TooOld {
	if len(m.byClient) == 0 {
		return nil
	}
	name, text, found := strings.Cut(strings.Trim(header, " \t"), "/")
	if !found {
		return nil
	}
	min, listed := m.byClient[name]
	if !listed {
		return nil
	}
	version, ok := ParseVersion(text)
	if !ok || version.Compare(min.version) >= 0 {
		return nil
	}
	return &TooOld{Client: name, Version: text, Minimum: min.text}
}

// Middleware answers a request from a client under its minimum with 426
// and the problem client_too_old, and passes every other request on. It
// is the next handler itself for a list that names no client.
func (m Minimums) Middleware(next http.Handler) http.Handler {
	if m.Empty() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refusal := m.Check(r.Header.Get(Header)); refusal != nil {
			response.RequestErrorWithDetails(w, r, http.StatusUpgradeRequired, refusal.Detail(), Code, refusal)
			return
		}
		next.ServeHTTP(w, r)
	})
}
