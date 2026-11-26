package ir

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The runtime extension points (D159). Core defines five interfaces that a
// running app calls, and every provider-specific piece ships as a package
// that implements one of them: a credential provider, a channel adapter, a
// sandbox provider, a model provider or a file processor. A package
// declares which interface it implements in its package.json, an API's
// config lists the packages it may use, and its @envVars class names a
// slot per provider it uses, a config field whose value in each
// environment names the provider (TypeDef.ProviderSlots). The TypeScript
// types of each interface are in @superschematic/http-runtime/providers.

// The runtime interfaces (D159).
const (
	// RuntimeCredentials is a credential provider: it lists the titles and
	// sites of the items a run may use, and resolves one into an injection
	// the runner applies, a browser fill or a request header. It never
	// returns a value to its caller.
	RuntimeCredentials = "credentials"
	// RuntimeChannel is a channel adapter: it sends a message, reads the
	// delivery status and the inbound messages its service posts to a
	// @webhook, and reports its health.
	RuntimeChannel = "channel"
	// RuntimeSandbox is a sandbox provider: it starts, cancels and reports
	// the status and the cost of a run's instance.
	RuntimeSandbox = "sandbox"
	// RuntimeModel is a model provider: it completes a turn with tools and
	// reports what the turn used.
	RuntimeModel = "model"
	// RuntimeFiles is a file processor: it extracts a file's text and
	// makes its preview.
	RuntimeFiles = "files"
)

// RuntimeContract is what a provider of one runtime interface implements:
// the operations core calls, each one it must implement and each it may.
// The TypeScript interface of the same name in
// @superschematic/http-runtime/providers has a method per operation;
// TestRuntimeContractsMatchTheTypeScriptRuntime keeps the two in step.
type RuntimeContract struct {
	// Interface is the interface's name, RuntimeChannel and the others.
	Interface string `json:"interface"`
	// Title is what a message calls a provider of it.
	Title string `json:"title"`
	// Required are the operations every provider implements.
	Required []string `json:"required"`
	// Optional are the operations a provider may implement and lists in
	// its manifest when it does.
	Optional []string `json:"optional,omitempty"`
}

// RuntimeContracts are the five runtime interfaces, in a fixed order.
var RuntimeContracts = []RuntimeContract{
	{Interface: RuntimeCredentials, Title: "credential provider", Required: []string{"list", "resolve"}},
	{Interface: RuntimeChannel, Title: "channel adapter", Required: []string{"send", "health"}, Optional: []string{"status", "inbound"}},
	{Interface: RuntimeSandbox, Title: "sandbox provider", Required: []string{"start", "cancel", "status", "cost"}},
	{Interface: RuntimeModel, Title: "model provider", Required: []string{"complete"}},
	{Interface: RuntimeFiles, Title: "file processor", Required: []string{"extractText", "preview"}},
}

// RuntimeContractOf returns the contract of the interface named name.
func RuntimeContractOf(name string) (RuntimeContract, bool) {
	for _, c := range RuntimeContracts {
		if c.Interface == name {
			return c, true
		}
	}
	return RuntimeContract{}, false
}

// RuntimeInterfaceNames are the interfaces' names, in RuntimeContracts'
// order.
func RuntimeInterfaceNames() []string {
	names := make([]string, len(RuntimeContracts))
	for i, c := range RuntimeContracts {
		names[i] = c.Interface
	}
	return names
}

// RuntimeProvider is a runtime provider package as its package.json
// declares it (D159), under "superschematic": {"provider": {...}}, with
// where an API's config found it.
type RuntimeProvider struct {
	// Name names the provider: an environment's slot holds it. Lower-case
	// letters, digits and hyphens. It is also its principal's subject,
	// "extension:<name>" (ExtensionSubject).
	Name string `json:"name"`

	// Implements is the runtime interface the provider implements.
	Implements string `json:"implements"`

	// Package is the package's name in its package.json.
	Package string `json:"package"`

	// Path is the package's directory as the API's config lists it,
	// relative to the service's directory, in slash form.
	Path string `json:"path"`

	// Operations are the operations the provider implements: every one its
	// interface requires, and the optional ones it has. The manifest may
	// leave out the required ones.
	Operations []string `json:"operations"`

	// Config are the settings the provider reads, each from the config
	// field named after the slot and the key (ProviderConfigField), sorted
	// by key.
	Config []ProviderConfigKey `json:"config,omitempty"`

	// Role are the permissions the provider's principal holds when it
	// calls the app, a messaging bridge relaying a reply, for one. Each
	// names one capability, two dotted segments or more, never an area.
	// None is a provider that never calls the app.
	Role []string `json:"role,omitempty"`
}

// ProviderConfigKey is one setting a runtime provider reads.
type ProviderConfigKey struct {
	// Key is the setting's name: upper-case letters, digits and
	// underscores. The provider of slot SMS reads key URL from the config
	// field SMS_URL.
	Key string `json:"key"`
	// Secret reports a setting the config holds as Secret<T>.
	Secret bool `json:"secret,omitempty"`
	// Optional reports a setting an environment may leave unset.
	Optional bool `json:"optional,omitempty"`
}

var (
	providerName  = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	providerKey   = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	permissionSeg = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
)

// Check checks the provider against its interface's contract: a name, an
// interface the core defines, every operation that interface requires and
// none it does not define, config keys of the right form, each once, and
// a narrow role. The error names the package.
func (p *RuntimeProvider) Check() error {
	where := fmt.Sprintf("runtime provider %q", p.Package)
	if p.Package == "" {
		where = "runtime provider at " + p.Path
	}
	if !providerName.MatchString(p.Name) {
		return fmt.Errorf("%s: name %q is lower-case letters and digits in words joined by hyphens (D159)", where, p.Name)
	}
	contract, ok := RuntimeContractOf(p.Implements)
	if !ok {
		return fmt.Errorf("%s: implements %q, which is no runtime interface; it is one of %s (D159)", where, p.Implements, strings.Join(RuntimeInterfaceNames(), ", "))
	}
	seen := map[string]bool{}
	for _, op := range p.Operations {
		if seen[op] {
			return fmt.Errorf("%s: lists operation %s twice", where, op)
		}
		seen[op] = true
		if !slices.Contains(contract.Required, op) && !slices.Contains(contract.Optional, op) {
			return fmt.Errorf("%s: a %s has no operation %s; its operations are %s (D159)", where, contract.Title, op, strings.Join(append(append([]string(nil), contract.Required...), contract.Optional...), ", "))
		}
	}
	var missing []string
	for _, op := range contract.Required {
		if !seen[op] {
			missing = append(missing, op)
		}
	}
	if len(missing) > 0 && len(p.Operations) > 0 {
		return fmt.Errorf("%s: a %s implements %s, and its operations leave out %s (D159)", where, contract.Title, strings.Join(contract.Required, ", "), strings.Join(missing, ", "))
	}
	keys := map[string]bool{}
	for _, c := range p.Config {
		if !providerKey.MatchString(c.Key) {
			return fmt.Errorf("%s: config key %q is upper-case letters, digits and underscores", where, c.Key)
		}
		if keys[c.Key] {
			return fmt.Errorf("%s: config key %s is declared twice", where, c.Key)
		}
		keys[c.Key] = true
	}
	for _, perm := range p.Role {
		segs := strings.Split(perm, ".")
		for _, seg := range segs {
			if !permissionSeg.MatchString(seg) {
				return fmt.Errorf("%s: role permission %q is a dotted path of words", where, perm)
			}
		}
		if len(segs) < 2 {
			return fmt.Errorf("%s: role permission %q names an area; a provider's role names one capability in it, such as %s.relay, so its principal covers no more (D159)", where, perm, perm)
		}
	}
	return nil
}

// Normalize fills Operations with the interface's required operations
// the manifest left out and sorts Operations and Config, after Check.
func (p *RuntimeProvider) Normalize() {
	contract, _ := RuntimeContractOf(p.Implements)
	for _, op := range contract.Required {
		if !slices.Contains(p.Operations, op) {
			p.Operations = append(p.Operations, op)
		}
	}
	order := append(append([]string(nil), contract.Required...), contract.Optional...)
	slices.SortStableFunc(p.Operations, func(a, b string) int { return slices.Index(order, a) - slices.Index(order, b) })
	slices.SortFunc(p.Config, func(a, b ProviderConfigKey) int { return strings.Compare(a.Key, b.Key) })
}

// ProviderConfigField is the config field a provider in slot reads its
// setting key from: SMS and URL give SMS_URL.
func ProviderConfigField(slot, key string) string {
	return slot + "_" + key
}

// ExtensionSubject is the subject of a runtime provider's principal
// (PrincipalExtension).
func ExtensionSubject(provider string) string {
	return "extension:" + provider
}

// Provider returns the provider of the schema named name, or nil.
func (s *Schema) Provider(name string) *RuntimeProvider {
	if s == nil {
		return nil
	}
	for _, p := range s.Providers {
		if p != nil && p.Name == name {
			return p
		}
	}
	return nil
}

// ProviderSlotsOf returns the provider slots of the schema's @envVars
// classes (TypeDef.ProviderSlots), by config field, with the interface
// each takes; nil for none. A class with @providers must be an @envVars
// class, and a slot two classes declare takes one interface.
func (s *Schema) ProviderSlotsOf() (map[string]string, error) {
	if s == nil {
		return nil, nil
	}
	var slots map[string]string
	from := map[string]string{}
	for _, name := range sortedStringMapKeys(s.Types) {
		td := s.Types[name]
		if td == nil || td.ProviderSlots == nil {
			continue
		}
		if !td.EnvVars {
			return nil, fmt.Errorf("%s: @providers is on %s, which is no @envVars class; a provider slot is a config field (D159)", s.Name, name)
		}
		for _, slot := range sortedStringMapKeys(td.ProviderSlots) {
			iface := td.ProviderSlots[slot]
			if prev, ok := slots[slot]; ok && prev != iface {
				return nil, fmt.Errorf("%s: slot %s takes %s on %s and %s on %s; a slot takes one interface (D159)", s.Name, slot, prev, from[slot], iface, name)
			}
			if slots == nil {
				slots = map[string]string{}
			}
			slots[slot], from[slot] = iface, name
		}
	}
	return slots, nil
}

// RuntimeFeatureUse is a feature a service uses that needs a runtime
// provider (D159): notifications need a channel adapter, agents a sandbox
// and a model provider. Each feature registers itself with the interface
// it needs (registry.RuntimeFeature), and an environment that runs the
// service with no provider of that interface is refused.
type RuntimeFeatureUse struct {
	// Feature is the feature's name.
	Feature string `json:"feature"`
	// Interface is the runtime interface it needs.
	Interface string `json:"interface"`
}
