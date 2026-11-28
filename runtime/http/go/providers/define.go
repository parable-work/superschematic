package providers

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.uber.org/zap"

	"github.com/parable-work/superschematic/runtime/http/go/session"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

// A provider package (D159, D184) exports the Definition Define checks:
// its name, the interface it implements, the settings it reads and its
// role, and Create, which makes the provider from its settings. Its
// directory declares the same, which the build reads and checks against
// the interface's contract; CheckManifest holds the two to each other in
// the package's own test.

// SettingSpec is one setting a provider reads, from the config field
// named after its slot and the key.
type SettingSpec struct {
	// Secret is a Secret<string> field: the provider gets a SecretValue,
	// never a string.
	Secret bool
	// Optional is a setting an environment may leave unset.
	Optional    bool
	Description string
}

// Context is what a provider runs with.
type Context struct {
	// Slot is the config field that names it.
	Slot string
	// Provider is its name.
	Provider string
	// Principal is its own principal (kind extension), whom core runs each
	// of its operations as; Role its role's permissions.
	Principal access.Principal
	Role      session.Role
	// Logger is a logger whose lines carry the provider and the slot.
	Logger *zap.Logger
}

// Definition is a provider package's definition.
type Definition struct {
	// Name is lower-case words joined by hyphens; an environment's slot
	// holds it.
	Name       string
	Implements Interface
	// Config are the settings it reads, by key: slot SMS and key URL read
	// SMS_URL.
	Config map[string]SettingSpec
	// Role are the permissions its principal holds, each one capability,
	// two dotted segments or more.
	Role []string
	// Create makes the provider, which implements the Go interface of
	// Implements (CheckInstance). From calls it once, on first use, with a
	// context of the provider's own principal.
	Create func(ctx context.Context, settings Settings, pc Context) (any, error)
}

// ContractError is a definition, a package or a provider that breaks its
// interface's contract.
type ContractError struct{ Message string }

func (e *ContractError) Error() string { return e.Message }

func contractErrorf(format string, args ...any) error {
	return &ContractError{Message: fmt.Sprintf(format, args...)}
}

var (
	nameForm    = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	keyForm     = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	segmentForm = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
)

// Define checks a definition as the build checks a manifest
// (ir.RuntimeProvider.Check) and returns it.
func Define(d Definition) (Definition, error) {
	where := fmt.Sprintf("provider %q", d.Name)
	if !nameForm.MatchString(d.Name) {
		return Definition{}, contractErrorf("%s: a name is lower-case letters and digits in words joined by hyphens", where)
	}
	if _, ok := ContractOf(d.Implements); !ok {
		names := make([]string, 0, len(Contracts()))
		for _, c := range Contracts() {
			names = append(names, c.Interface)
		}
		return Definition{}, contractErrorf("%s: implements %q, which is no runtime interface; it is one of %s", where, string(d.Implements), strings.Join(names, ", "))
	}
	for key := range d.Config {
		if !keyForm.MatchString(key) {
			return Definition{}, contractErrorf("%s: config key %q is upper-case letters, digits and underscores", where, key)
		}
		if key == CallerKey && !d.Config[key].Secret {
			return Definition{}, contractErrorf("%s: config key %s is the key the provider calls the app with, which an environment sets as a secret; declare it Secret (#744)", where, key)
		}
	}
	for _, permission := range d.Role {
		segments := strings.Split(permission, ".")
		for _, segment := range segments {
			if !segmentForm.MatchString(segment) {
				return Definition{}, contractErrorf("%s: role permission %q is a dotted path of words", where, permission)
			}
		}
		if len(segments) < 2 {
			return Definition{}, contractErrorf("%s: role permission %q names an area; a provider's role names one capability in it", where, permission)
		}
	}
	if d.Create == nil {
		return Definition{}, contractErrorf("%s: create is not a function", where)
	}
	out := d
	out.Config = map[string]SettingSpec{}
	for k, v := range d.Config {
		out.Config[k] = v
	}
	out.Role = slices.Clone(d.Role)
	return out, nil
}

// MustDefine is Define, panicking on a definition that breaks its
// contract: what a provider package's Definition returns.
func MustDefine(d Definition) Definition {
	out, err := Define(d)
	if err != nil {
		panic(err)
	}
	return out
}

// operations are the Go interfaces of each operation of each runtime
// interface: an instance has the operation when it implements the
// interface.
func has(iface Interface, op string, instance any) bool {
	switch iface {
	case Credentials:
		_, ok := instance.(CredentialProvider)
		return ok && (op == "list" || op == "resolve")
	case Channel:
		switch op {
		case "send", "health":
			_, ok := instance.(ChannelAdapter)
			return ok
		case "status":
			_, ok := instance.(ChannelStatusReader)
			return ok
		case "inbound":
			_, ok := instance.(ChannelInboundReader)
			return ok
		}
	case Sandbox:
		_, ok := instance.(SandboxProvider)
		return ok && (op == "start" || op == "cancel" || op == "status" || op == "cost")
	case Model:
		_, ok := instance.(ModelProvider)
		return ok && op == "complete"
	case Files:
		_, ok := instance.(FileProcessor)
		return ok && (op == "extractText" || op == "preview")
	}
	return false
}

// goInterfaceOf names the Go interface a provider of iface implements.
func goInterfaceOf(iface Interface) string {
	switch iface {
	case Credentials:
		return "providers.CredentialProvider"
	case Channel:
		return "providers.ChannelAdapter"
	case Sandbox:
		return "providers.SandboxProvider"
	case Model:
		return "providers.ModelProvider"
	case Files:
		return "providers.FileProcessor"
	}
	return string(iface)
}

// CheckInstance is the operations of its interface instance has: each one
// it requires, which it must have, and the optional ones it has.
func CheckInstance(d Definition, instance any) ([]string, error) {
	contract, ok := ContractOf(d.Implements)
	if !ok {
		return nil, contractErrorf("provider %s implements %q, which is no runtime interface", d.Name, string(d.Implements))
	}
	where := contract.Title + " " + d.Name
	if instance == nil {
		return nil, contractErrorf("%s: create made no provider", where)
	}
	var missing []string
	for _, op := range contract.Required {
		if !has(d.Implements, op, instance) {
			missing = append(missing, op)
		}
	}
	if len(missing) > 0 {
		return nil, contractErrorf("%s: implements no %s, which every %s implements (a %T is no %s)", where, strings.Join(missing, ", "), contract.Title, instance, goInterfaceOf(d.Implements))
	}
	out := slices.Clone(contract.Required)
	for _, op := range contract.Optional {
		if has(d.Implements, op, instance) {
			out = append(out, op)
		}
	}
	return out, nil
}

// Manifest is a provider's declaration as its directory holds it: the
// "superschematic": {"provider": ...} object of a package.json, or the
// "provider" object of a superschematic.json.
type Manifest struct {
	Name       string                  `json:"name"`
	Implements string                  `json:"implements"`
	Operations []string                `json:"operations,omitempty"`
	Config     map[string]ManifestItem `json:"config,omitempty"`
	Role       []string                `json:"role,omitempty"`
}

// ManifestItem is one setting of a manifest's config.
type ManifestItem struct {
	Secret      bool   `json:"secret,omitempty"`
	Optional    bool   `json:"optional,omitempty"`
	Description string `json:"description,omitempty"`
}

// ReadManifest reads the provider dir declares, from its
// superschematic.json or its package.json, which, when both declare one,
// declare the same.
func ReadManifest(dir string) (Manifest, error) {
	var found []Manifest
	if data, err := os.ReadFile(filepath.Join(dir, "superschematic.json")); err == nil {
		var doc struct {
			Provider *Manifest `json:"provider"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			return Manifest{}, fmt.Errorf("%s: superschematic.json: %w", dir, err)
		}
		if doc.Provider == nil {
			return Manifest{}, fmt.Errorf("%s: superschematic.json declares no provider", dir)
		}
		found = append(found, *doc.Provider)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, err
	}
	if data, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var doc struct {
			Superschematic struct {
				Provider *Manifest `json:"provider"`
			} `json:"superschematic"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			return Manifest{}, fmt.Errorf("%s: package.json: %w", dir, err)
		}
		if doc.Superschematic.Provider != nil {
			found = append(found, *doc.Superschematic.Provider)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, err
	}
	switch len(found) {
	case 0:
		return Manifest{}, fmt.Errorf("%s declares no provider: a Go provider's directory declares it in its superschematic.json ({\"provider\": ...}) or its package.json (D159, D184)", dir)
	case 2:
		a, _ := json.Marshal(found[0].normal())
		b, _ := json.Marshal(found[1].normal())
		if string(a) != string(b) {
			return Manifest{}, fmt.Errorf("%s: superschematic.json and package.json declare different providers (%s and %s)", dir, a, b)
		}
	}
	return found[0], nil
}

// normal is the manifest with its lists sorted and its descriptions left
// out, as two declarations of one provider compare.
func (m Manifest) normal() Manifest {
	out := m
	out.Operations = slices.Clone(m.Operations)
	sort.Strings(out.Operations)
	out.Role = slices.Clone(m.Role)
	sort.Strings(out.Role)
	out.Config = map[string]ManifestItem{}
	for k, v := range m.Config {
		out.Config[k] = ManifestItem{Secret: v.Secret, Optional: v.Optional}
	}
	return out
}

// CheckManifest holds a provider's manifest to its definition and, given
// one, to a provider it made: the name, the interface, the settings, the
// role, and the operations, which the manifest lists or leaves to the
// required ones. A package's own test calls it, so the build and the
// runtime read one provider.
func CheckManifest(manifest Manifest, d Definition, instance any) error {
	where := "package of provider " + d.Name
	if manifest.Name != d.Name {
		return contractErrorf("%s: its manifest names %q", where, manifest.Name)
	}
	if manifest.Implements != string(d.Implements) {
		return contractErrorf("%s: its manifest implements %q, the definition %s", where, manifest.Implements, d.Implements)
	}
	settings := func(keys map[string][2]bool) string {
		names := make([]string, 0, len(keys))
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)
		parts := make([]string, len(names))
		for i, k := range names {
			parts[i] = fmt.Sprintf("[%q,%t,%t]", k, keys[k][0], keys[k][1])
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	declared := map[string][2]bool{}
	for k, v := range manifest.Config {
		declared[k] = [2]bool{v.Secret, v.Optional}
	}
	defined := map[string][2]bool{}
	for k, v := range d.Config {
		defined[k] = [2]bool{v.Secret, v.Optional}
	}
	if a, b := settings(declared), settings(defined); a != b {
		return contractErrorf("%s: its manifest's config %s is not the definition's %s", where, a, b)
	}
	sorted := func(list []string) string {
		s := slices.Clone(list)
		sort.Strings(s)
		data, _ := json.Marshal(append([]string{}, s...))
		return string(data)
	}
	if a, b := sorted(manifest.Role), sorted(d.Role); a != b {
		return contractErrorf("%s: its manifest's role %s is not the definition's %s", where, a, b)
	}
	if instance != nil {
		ops, err := CheckInstance(d, instance)
		if err != nil {
			return err
		}
		listed := manifest.Operations
		if listed == nil {
			contract, _ := ContractOf(d.Implements)
			listed = contract.Required
		}
		if a, b := sorted(listed), sorted(ops); a != b {
			return contractErrorf("%s: its manifest lists operations %s, and the provider has %s", where, a, b)
		}
	}
	return nil
}

// ExtensionPrincipal is the principal a provider acts as (D159): kind
// extension, subject extension:<name>.
func ExtensionPrincipal(name string) access.Principal {
	return access.Principal{Subject: ExtensionSubject(name), Kind: access.KindExtension}
}

// ExtensionRole is the one role a provider's principal holds: its role's
// permissions alone, named after its principal.
func ExtensionRole(name string, role []string) session.Role {
	return session.Role{Name: ExtensionSubject(name), Permissions: slices.Clone(role)}
}

// ExtensionKey is a key a provider calls the app with, and the provider
// it is.
type ExtensionKey struct {
	Key      SecretValue
	Provider string
	Role     []string
}

// ExtensionAuthenticator is the auth step of the providers that call the
// app over HTTP, a messaging bridge that connects outward and relays
// replies, for one (D159): a request whose Bearer token equals one of keys
// runs as that provider's principal, with its role's permissions alone;
// every other request is inner's to establish, or has no principal when
// inner is nil. It composes with session.GrantAuthenticator as the inner
// of either.
func ExtensionAuthenticator(keys []ExtensionKey, inner func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		other := next
		if inner != nil {
			other = inner(next)
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := session.BearerToken(r.Header)
			if ok {
				for _, key := range keys {
					want := key.Key.Reveal()
					if want != "" && subtle.ConstantTimeCompare([]byte(token), []byte(want)) == 1 {
						ctx := session.ContextWithCaller(r.Context(), ExtensionPrincipal(key.Provider))
						ctx = session.ContextWithRoles(ctx, []session.Role{ExtensionRole(key.Provider, key.Role)})
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
				}
			}
			other.ServeHTTP(w, r)
		})
	}
}

// SettingError is a setting missing, or read as what it is not.
type SettingError struct{ Message string }

func (e *SettingError) Error() string { return e.Message }

// ConfigValue is one field of an API's loaded config as From reads it: a
// plain text or a secret. The zero ConfigValue is a field unset.
type ConfigValue struct {
	text   string
	secret bool
	set    bool
}

// Text is a plain config field's value; "" is a field unset.
func Text(text string) ConfigValue { return ConfigValue{text: text, set: text != ""} }

// Secret is a Secret<string> config field's value; "" is a field unset.
func Secret(text string) ConfigValue { return ConfigValue{text: text, secret: true, set: text != ""} }

// IsSet reports whether the field holds a value.
func (v ConfigValue) IsSet() bool { return v.set }

// IsSecret reports whether the field is a Secret<string>.
func (v ConfigValue) IsSecret() bool { return v.secret }

// String is the value of a plain field, SecretMask for a secret one.
func (v ConfigValue) String() string {
	if v.secret {
		return SecretMask
	}
	return v.text
}

// GoString is String, for %#v.
func (v ConfigValue) GoString() string { return v.String() }

// Format prints String for every verb.
func (v ConfigValue) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(v.String())) }

// MarshalJSON is String, as a JSON string.
func (v ConfigValue) MarshalJSON() ([]byte, error) { return json.Marshal(v.String()) }

// Settings are a provider's settings: the config fields named after its
// slot, by key. A plain setting reads as a string and a secret one as a
// SecretValue, so a provider never holds a secret as a plain string. JSON
// and fmt print the keys and masked secrets.
type Settings struct {
	slot   string
	values map[string]ConfigValue
}

// NewSettings is the settings of a provider in slot, by key: what From
// gives Create, and what a provider's own test makes one with.
func NewSettings(slot string, values map[string]ConfigValue) Settings {
	out := map[string]ConfigValue{}
	for k, v := range values {
		if v.set {
			out[k] = v
		}
	}
	return Settings{slot: slot, values: out}
}

// Has reports whether the environment set key.
func (s Settings) Has(key string) bool {
	_, ok := s.values[key]
	return ok
}

// Text is the plain setting key; it fails for one unset or secret.
func (s Settings) Text(key string) (string, error) {
	value, ok, err := s.OptionalText(key)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", &SettingError{Message: fmt.Sprintf("setting %s of slot %s is not set", key, s.slot)}
	}
	return value, nil
}

// OptionalText is the plain setting key, and false when unset; it fails
// for a secret one.
func (s Settings) OptionalText(key string) (string, bool, error) {
	value, ok := s.values[key]
	if !ok {
		return "", false, nil
	}
	if value.secret {
		return "", false, &SettingError{Message: fmt.Sprintf("setting %s of slot %s is a secret; read it with Secret", key, s.slot)}
	}
	return value.text, true, nil
}

// Secret is the secret setting key; it fails for one unset or plain.
func (s Settings) Secret(key string) (SecretValue, error) {
	value, ok := s.values[key]
	if !ok {
		return SecretValue{}, &SettingError{Message: fmt.Sprintf("setting %s of slot %s is not set", key, s.slot)}
	}
	if !value.secret {
		return SecretValue{}, &SettingError{Message: fmt.Sprintf("setting %s of slot %s is no secret; read it with Text", key, s.slot)}
	}
	return NewSecret(value.text), nil
}

// MarshalJSON is the keys, with secrets masked.
func (s Settings) MarshalJSON() ([]byte, error) {
	out := map[string]string{}
	for k, v := range s.values {
		out[k] = v.String()
	}
	return json.Marshal(out)
}

// String names the slot and the keys.
func (s Settings) String() string {
	keys := make([]string, 0, len(s.values))
	for k := range s.values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return fmt.Sprintf("ProviderSettings(%s: %s)", s.slot, strings.Join(keys, ", "))
}

// GoString is String, for %#v.
func (s Settings) GoString() string { return s.String() }

// Format prints String for every verb.
func (s Settings) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(s.String())) }
