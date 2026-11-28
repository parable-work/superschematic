package providers

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/parable-work/superschematic/runtime/http/go/session"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

// How core obtains the provider an environment configures (D159, D184).
// The API's @envVars class names its slots with @providers, and the stack
// binds each slot, a config field, to a provider's name in each
// environment, with the provider's settings in the fields named after the
// slot. From reads the loaded config with the slots and the definitions
// the server links, and answers Providers: Channel(ctx, "SMS") gives the
// channel adapter slot SMS names, made on first use from its settings.
// Each call core makes into a provider runs within the provider's own
// principal: its context carries the principal and its role, so what it
// writes through an ORM opened with no user is recorded as
// extension:<name>.
//
// The build refuses an environment whose slot names an unknown provider or
// leaves a needed setting unbound; From refuses the same at start, with
// every problem and never a value, for a config no build checked.

// Options are what From reads.
type Options struct {
	// Slots are the API's provider slots, as @providers declares them:
	// SMS takes Channel.
	Slots map[string]Interface
	// Config is the API's loaded config: each slot's field names a
	// provider, and the fields after it hold its settings, by field name.
	// A field unset is absent or the zero ConfigValue.
	Config map[string]ConfigValue
	// Definitions are the definitions of the provider modules the server
	// links, the ones the API's config lists.
	Definitions []Definition
	// Manifests are the manifests the build read of the providers the
	// API's config lists, by name: each definition is held to its own
	// (CheckManifest). Nil for a server no build wrote.
	Manifests map[string]Manifest
	// Logger is the logger each provider's context derives its own from;
	// nil is a logger that writes nothing.
	Logger *zap.Logger
	// Bind returns ctx carrying principal, a provider's, with role its one
	// role: what every call into the provider is made with. Nil binds the
	// session's caller and roles (session.ContextWithCaller,
	// ContextWithRoles); a generated server binds its ORM's principal too
	// (orm.WithPrincipal).
	Bind func(ctx context.Context, principal access.Principal, role session.Role) context.Context
}

// ConfiguredProvider is a slot an environment fills.
type ConfiguredProvider struct {
	Slot      string    `json:"slot"`
	Interface Interface `json:"interface"`
	Provider  string    `json:"provider"`
}

// ConfigError is a config that names an unknown provider, one of another
// interface, or leaves a setting unset: every problem, and never a value.
type ConfigError struct{ Problems []string }

func (e *ConfigError) Error() string {
	return "runtime providers: " + strings.Join(e.Problems, "; ")
}

// NotConfiguredError is asked for a slot the environment leaves unset, or
// one of another interface.
type NotConfiguredError struct{ Message string }

func (e *NotConfiguredError) Error() string { return e.Message }

type entry struct {
	slot       string
	iface      Interface
	definition Definition
	settings   Settings
	principal  access.Principal
	role       session.Role

	once     sync.Once
	instance any
	err      error
}

// Providers are the providers an environment configures. A server holds
// one, shared, in its Deps; it is safe for concurrent use.
type Providers struct {
	slots   map[string]Interface
	entries map[string]*entry
	logger  *zap.Logger
	bind    func(ctx context.Context, principal access.Principal, role session.Role) context.Context
}

// From reads the providers options.Config configures; it fails with a
// *ConfigError naming every problem.
func From(options Options) (*Providers, error) {
	var problems []string
	byName := map[string]Definition{}
	for _, d := range options.Definitions {
		if _, ok := byName[d.Name]; ok {
			problems = append(problems, "two linked providers are named "+d.Name)
		}
		if m, ok := options.Manifests[d.Name]; ok {
			if err := CheckManifest(m, d, nil); err != nil {
				problems = append(problems, err.Error())
			}
		}
		byName[d.Name] = d
	}
	for _, name := range slices.Sorted(maps.Keys(options.Manifests)) {
		if _, ok := byName[name]; !ok {
			problems = append(problems, fmt.Sprintf("the API's config lists provider %s, which the server does not link", name))
		}
	}
	entries := map[string]*entry{}
	for _, slot := range slices.Sorted(maps.Keys(options.Slots)) {
		iface := options.Slots[slot]
		contract, ok := ContractOf(iface)
		if !ok {
			problems = append(problems, fmt.Sprintf("slot %s takes %q, which is no runtime interface", slot, string(iface)))
			continue
		}
		named := options.Config[slot]
		if !named.set {
			continue
		}
		if named.secret {
			problems = append(problems, fmt.Sprintf("slot %s holds no provider's name", slot))
			continue
		}
		name := named.text
		d, ok := byName[name]
		if !ok {
			links := strings.Join(slices.Sorted(maps.Keys(byName)), ", ")
			if links == "" {
				links = "none"
			}
			problems = append(problems, fmt.Sprintf("slot %s names provider %s, which the server does not link (it links %s)", slot, name, links))
			continue
		}
		if d.Implements != iface {
			problems = append(problems, fmt.Sprintf("slot %s takes a %s, and provider %s implements %s", slot, contract.Title, name, d.Implements))
			continue
		}
		values := map[string]ConfigValue{}
		for _, key := range slices.Sorted(maps.Keys(d.Config)) {
			spec := d.Config[key]
			field := ConfigField(slot, key)
			value := options.Config[field]
			if !value.set {
				if !spec.Optional {
					problems = append(problems, fmt.Sprintf("provider %s in slot %s needs %s, which is not set", name, slot, field))
				}
				continue
			}
			if spec.Secret != value.secret {
				what := func(secret bool) string {
					if secret {
						return "a secret"
					}
					return "no secret"
				}
				problems = append(problems, fmt.Sprintf("provider %s in slot %s reads %s as %s, and the config holds it as %s", name, slot, field, what(spec.Secret), what(value.secret)))
				continue
			}
			values[key] = value
		}
		entries[slot] = &entry{
			slot:       slot,
			iface:      iface,
			definition: d,
			settings:   NewSettings(slot, values),
			principal:  ExtensionPrincipal(d.Name),
			role:       ExtensionRole(d.Name, d.Role),
		}
	}
	if len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	logger := options.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	bind := options.Bind
	if bind == nil {
		bind = func(ctx context.Context, principal access.Principal, role session.Role) context.Context {
			ctx = session.ContextWithCaller(ctx, principal)
			return session.ContextWithRoles(ctx, []session.Role{role})
		}
	}
	slots := map[string]Interface{}
	for k, v := range options.Slots {
		slots[k] = v
	}
	return &Providers{slots: slots, entries: entries, logger: logger, bind: bind}, nil
}

// ExtensionKeysOf are the keys the providers options.Config configures
// call the app with (#744): one per slot whose provider lists CallerKey
// and whose <SLOT>_CALLER_KEY the config sets, a secret, with the
// provider's name and role, sorted by slot. A server wraps its routes'
// auth step in ExtensionAuthenticator over them, outermost. It reads what
// From reads and checks nothing itself: a server builds Deps.Providers
// with From beside it, which refuses a config that names an unknown
// provider or holds the key as no secret.
func ExtensionKeysOf(options Options) []ExtensionKey {
	byName := map[string]Definition{}
	for _, d := range options.Definitions {
		byName[d.Name] = d
	}
	var keys []ExtensionKey
	for _, slot := range slices.Sorted(maps.Keys(options.Slots)) {
		named := options.Config[slot]
		if !named.set || named.secret {
			continue
		}
		d, ok := byName[named.text]
		if !ok || d.Implements != options.Slots[slot] {
			continue
		}
		if _, calls := d.Config[CallerKey]; !calls {
			continue
		}
		key := options.Config[ConfigField(slot, CallerKey)]
		if !key.set || !key.secret {
			continue
		}
		keys = append(keys, ExtensionKey{Key: NewSecret(key.text), Provider: d.Name, Role: slices.Clone(d.Role)})
	}
	return keys
}

// Configured are the slots the environment fills, of iface or, for "", of
// every interface, sorted by slot.
func (p *Providers) Configured(iface Interface) []ConfiguredProvider {
	var out []ConfiguredProvider
	for _, slot := range slices.Sorted(maps.Keys(p.entries)) {
		e := p.entries[slot]
		if iface == "" || e.iface == iface {
			out = append(out, ConfiguredProvider{Slot: slot, Interface: e.iface, Provider: e.definition.Name})
		}
	}
	return out
}

func (p *Providers) entryOf(slot string, iface Interface) (*entry, error) {
	if p == nil {
		return nil, &NotConfiguredError{Message: slot + " is no provider slot of the API"}
	}
	e, ok := p.entries[slot]
	if !ok {
		if _, declared := p.slots[slot]; declared {
			return nil, &NotConfiguredError{Message: fmt.Sprintf("slot %s names no provider in this environment", slot)}
		}
		return nil, &NotConfiguredError{Message: slot + " is no provider slot of the API"}
	}
	if iface != "" && e.iface != iface {
		return nil, &NotConfiguredError{Message: fmt.Sprintf("slot %s takes a %s, not a %s", slot, e.iface.title(), iface.title())}
	}
	return e, nil
}

// PrincipalOf is the principal of the provider slot names.
func (p *Providers) PrincipalOf(slot string) (access.Principal, error) {
	e, err := p.entryOf(slot, "")
	if err != nil {
		return access.Principal{}, err
	}
	return e.principal, nil
}

// Within returns ctx as the provider slot names: its principal and its
// role, as every call into it is made. A route that handles what the
// provider posts back, its inbound messages, runs within it.
func (p *Providers) Within(ctx context.Context, slot string) (context.Context, error) {
	e, err := p.entryOf(slot, "")
	if err != nil {
		return ctx, err
	}
	return p.bind(ctx, e.principal, e.role), nil
}

// Get is the provider of slot, which takes iface, made on first use; it
// fails with a *NotConfiguredError for a slot unset. Each of its
// operations runs within the provider's principal.
func (p *Providers) Get(ctx context.Context, slot string, iface Interface) (any, error) {
	e, err := p.entryOf(slot, iface)
	if err != nil {
		return nil, err
	}
	e.once.Do(func() {
		pc := Context{
			Slot:      e.slot,
			Provider:  e.definition.Name,
			Principal: e.principal,
			Role:      e.role,
			Logger:    p.logger.With(zap.String("provider", e.definition.Name), zap.String("slot", e.slot)),
		}
		// The provider outlives the call that first asks for it, so it is
		// made with no deadline of that call's.
		made, err := e.definition.Create(p.bind(context.WithoutCancel(ctx), e.principal, e.role), e.settings, pc)
		if err != nil {
			e.err = fmt.Errorf("%s %s in slot %s: %w", e.iface.title(), e.definition.Name, e.slot, err)
			return
		}
		if _, err := CheckInstance(e.definition, made); err != nil {
			e.err = err
			return
		}
		within := func(ctx context.Context) context.Context { return p.bind(ctx, e.principal, e.role) }
		e.instance = bound(e.iface, made, within)
	})
	return e.instance, e.err
}

// Credentials is the credential provider of slot.
func (p *Providers) Credentials(ctx context.Context, slot string) (CredentialProvider, error) {
	got, err := p.Get(ctx, slot, Credentials)
	if err != nil {
		return nil, err
	}
	return got.(CredentialProvider), nil
}

// Channel is the channel adapter of slot. It implements
// ChannelStatusReader and ChannelInboundReader when the adapter does.
func (p *Providers) Channel(ctx context.Context, slot string) (ChannelAdapter, error) {
	got, err := p.Get(ctx, slot, Channel)
	if err != nil {
		return nil, err
	}
	return got.(ChannelAdapter), nil
}

// Sandbox is the sandbox provider of slot.
func (p *Providers) Sandbox(ctx context.Context, slot string) (SandboxProvider, error) {
	got, err := p.Get(ctx, slot, Sandbox)
	if err != nil {
		return nil, err
	}
	return got.(SandboxProvider), nil
}

// Model is the model provider of slot. It implements ModelEmbedder when
// the provider does.
func (p *Providers) Model(ctx context.Context, slot string) (ModelProvider, error) {
	got, err := p.Get(ctx, slot, Model)
	if err != nil {
		return nil, err
	}
	return got.(ModelProvider), nil
}

// Files is the file processor of slot.
func (p *Providers) Files(ctx context.Context, slot string) (FileProcessor, error) {
	got, err := p.Get(ctx, slot, Files)
	if err != nil {
		return nil, err
	}
	return got.(FileProcessor), nil
}

// IsNotConfigured reports whether err is a slot the environment leaves
// unset.
func IsNotConfigured(err error) bool {
	var target *NotConfiguredError
	return errors.As(err, &target)
}

// bound is made, a provider of iface, with every operation made within
// its principal, and the optional operations it has kept.
func bound(iface Interface, made any, within func(context.Context) context.Context) any {
	switch iface {
	case Credentials:
		return &credentialsAs{made.(CredentialProvider), within}
	case Channel:
		base := channelAs{made.(ChannelAdapter), within}
		status, hasStatus := made.(ChannelStatusReader)
		inbound, hasInbound := made.(ChannelInboundReader)
		switch {
		case hasStatus && hasInbound:
			return &channelWithBoth{channelWithStatus{base, status}, inbound}
		case hasStatus:
			return &channelWithStatus{base, status}
		case hasInbound:
			return &channelWithInbound{base, inbound}
		}
		return &base
	case Sandbox:
		return &sandboxAs{made.(SandboxProvider), within}
	case Model:
		base := modelAs{made.(ModelProvider), within}
		if embed, ok := made.(ModelEmbedder); ok {
			return &modelWithEmbed{base, embed}
		}
		return &base
	case Files:
		return &filesAs{made.(FileProcessor), within}
	}
	return made
}

type credentialsAs struct {
	inner  CredentialProvider
	within func(context.Context) context.Context
}

func (c credentialsAs) List(ctx context.Context, query CredentialQuery) ([]CredentialListing, error) {
	return c.inner.List(c.within(ctx), query)
}

func (c credentialsAs) Resolve(ctx context.Context, ref string, target InjectionTarget) (*Injection, error) {
	return c.inner.Resolve(c.within(ctx), ref, target)
}

type channelAs struct {
	inner  ChannelAdapter
	within func(context.Context) context.Context
}

func (c channelAs) Kind() string { return c.inner.Kind() }

func (c channelAs) Send(ctx context.Context, message OutboundMessage) (SendResult, error) {
	return c.inner.Send(c.within(ctx), message)
}

func (c channelAs) Health(ctx context.Context) (ChannelHealth, error) {
	return c.inner.Health(c.within(ctx))
}

type channelWithStatus struct {
	channelAs
	status ChannelStatusReader
}

func (c channelWithStatus) Status(ctx context.Context, request WebhookRequest) ([]DeliveryStatus, error) {
	return c.status.Status(c.within(ctx), request)
}

type channelWithInbound struct {
	channelAs
	inbound ChannelInboundReader
}

func (c channelWithInbound) Inbound(ctx context.Context, request WebhookRequest) ([]InboundMessage, error) {
	return c.inbound.Inbound(c.within(ctx), request)
}

type channelWithBoth struct {
	channelWithStatus
	inbound ChannelInboundReader
}

func (c channelWithBoth) Inbound(ctx context.Context, request WebhookRequest) ([]InboundMessage, error) {
	return c.inbound.Inbound(c.within(ctx), request)
}

type sandboxAs struct {
	inner  SandboxProvider
	within func(context.Context) context.Context
}

func (s sandboxAs) Start(ctx context.Context, spec SandboxSpec) (SandboxInstance, error) {
	return s.inner.Start(s.within(ctx), spec)
}

func (s sandboxAs) Cancel(ctx context.Context, id string) error {
	return s.inner.Cancel(s.within(ctx), id)
}

func (s sandboxAs) Status(ctx context.Context, id string) (SandboxStatus, error) {
	return s.inner.Status(s.within(ctx), id)
}

func (s sandboxAs) Cost(ctx context.Context, id string) (SandboxCost, error) {
	return s.inner.Cost(s.within(ctx), id)
}

type modelAs struct {
	inner  ModelProvider
	within func(context.Context) context.Context
}

func (m modelAs) Complete(ctx context.Context, turn ModelTurn) (ModelCompletion, error) {
	return m.inner.Complete(m.within(ctx), turn)
}

type modelWithEmbed struct {
	modelAs
	embed ModelEmbedder
}

func (m modelWithEmbed) Embed(ctx context.Context, request ModelEmbedRequest) (ModelEmbedding, error) {
	return m.embed.Embed(m.within(ctx), request)
}

type filesAs struct {
	inner  FileProcessor
	within func(context.Context) context.Context
}

func (f filesAs) MediaTypes() []string { return f.inner.MediaTypes() }

func (f filesAs) ExtractText(ctx context.Context, file FileInput) (*ExtractedText, error) {
	return f.inner.ExtractText(f.within(ctx), file)
}

func (f filesAs) Preview(ctx context.Context, file FileInput, options PreviewOptions) (*Preview, error) {
	return f.inner.Preview(f.within(ctx), file, options)
}
