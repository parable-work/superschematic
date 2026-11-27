package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// The credential provider (D159). Core never holds a credential's value:
// List gives the titles and sites of the items a run may use, and Resolve
// gives an Injection, which carries the value sealed. Only ApplyInjection
// opens it, handing it to the runner that uses it, a browser that fills a
// field or a client that sets a request header, inside the process that
// runs the step. An Injection's JSON, its fmt verbs and its String print
// its reference and target alone, so a log or a run record that holds one
// holds no value. UseCredential does the three steps and returns what a
// run record keeps: the reference, the provider, the target and the time
// it was used.

// CredentialListing is an item a credential provider holds, as List gives
// it: never its value.
type CredentialListing struct {
	// Ref is the secret reference the item is named by, in the provider's
	// own form, such as "vault/item/field".
	Ref string `json:"ref"`
	// Title is what a person calls the item.
	Title string `json:"title"`
	// Sites are the sites the item may be used on, by origin or host.
	Sites []string `json:"sites"`
}

// CredentialQuery is what List narrows to: the items usable on one site.
type CredentialQuery struct {
	Site string `json:"site,omitempty"`
}

// The kinds of injection target.
const (
	TargetFill   = "fill"
	TargetHeader = "header"
)

// InjectionTarget is where an injection goes: a field a browser fills
// (Kind fill, Selector), or a header a request carries (Kind header,
// Name), on one site.
type InjectionTarget struct {
	Kind     string `json:"kind"`
	Site     string `json:"site"`
	Selector string `json:"selector,omitempty"`
	Name     string `json:"name,omitempty"`
}

// FillTarget is the target of the field selector names on site.
func FillTarget(site, selector string) InjectionTarget {
	return InjectionTarget{Kind: TargetFill, Site: site, Selector: selector}
}

// HeaderTarget is the target of the header name of a request to site.
func HeaderTarget(site, name string) InjectionTarget {
	return InjectionTarget{Kind: TargetHeader, Site: site, Name: name}
}

// normal is the target with only the members of its kind.
func (t InjectionTarget) normal() InjectionTarget {
	switch t.Kind {
	case TargetFill:
		return InjectionTarget{Kind: TargetFill, Site: t.Site, Selector: t.Selector}
	case TargetHeader:
		return InjectionTarget{Kind: TargetHeader, Site: t.Site, Name: t.Name}
	}
	return t
}

// InjectionRunner is what applies an injection: the runner of a step,
// inside the process that uses the value.
type InjectionRunner interface {
	// Fill fills a browser field with the value.
	Fill(ctx context.Context, selector, value string) error
	// SetHeader sets a header of the request the runner sends.
	SetHeader(ctx context.Context, name, value string) error
}

// CredentialUse is what a run record keeps of a credential's use (D159):
// its reference, never its value.
type CredentialUse struct {
	Ref string `json:"ref"`
	// Provider is the provider's name.
	Provider string          `json:"provider"`
	Target   InjectionTarget `json:"target"`
	UsedAt   time.Time       `json:"usedAt"`
}

// CredentialProvider is a credential provider: password managers and
// secret brokers implement it. Neither operation gives core a value. A
// provider refuses a reference its scope does not hold, and one whose
// item is not for the target's site.
type CredentialProvider interface {
	// List is the items a run may use, narrowed to a site: titles and
	// sites only.
	List(ctx context.Context, query CredentialQuery) ([]CredentialListing, error)
	// Resolve is the item ref names, sealed in an injection into target.
	Resolve(ctx context.Context, ref string, target InjectionTarget) (*Injection, error)
}

// Injection is a credential's value on its way to the runner, sealed: no
// method of it returns the value, and only ApplyInjection, of this
// package, opens it. A provider makes one with FillInjection or
// HeaderInjection from a SecretValue, so the value is never a plain
// string in its hands either. Its fields are unexported, so no other
// package makes one that holds a value: an Injection{} of another package
// is refused.
type Injection struct {
	ref    string
	target InjectionTarget
	value  *SecretValue
}

// FillInjection is an injection that fills the field selector names on
// site with value.
func FillInjection(ref, site, selector string, value SecretValue) *Injection {
	return &Injection{ref: ref, target: FillTarget(site, selector), value: &value}
}

// HeaderInjection is an injection that sets the header name of a request
// to site to value.
func HeaderInjection(ref, site, name string, value SecretValue) *Injection {
	return &Injection{ref: ref, target: HeaderTarget(site, name), value: &value}
}

// Ref is the reference the injection resolves.
func (i *Injection) Ref() string { return i.ref }

// Target is where the injection goes.
func (i *Injection) Target() InjectionTarget { return i.target }

// String names the injection's kind and reference.
func (i *Injection) String() string { return fmt.Sprintf("Injection(%s %s)", i.target.Kind, i.ref) }

// GoString is String, for %#v.
func (i *Injection) GoString() string { return i.String() }

// Format prints String for every verb.
func (i *Injection) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(i.String())) }

// MarshalJSON is the reference, the target and the value masked.
func (i *Injection) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Ref    string          `json:"ref"`
		Target InjectionTarget `json:"target"`
		Value  string          `json:"value"`
	}{i.ref, i.target, SecretMask})
}

// InjectionError refuses an injection: not one a credential provider
// sealed, or not the reference and target asked for.
type InjectionError struct{ Message string }

func (e *InjectionError) Error() string { return e.Message }

// ApplyInjection hands injection's value to runner, the one place it is
// opened.
func ApplyInjection(ctx context.Context, injection *Injection, runner InjectionRunner) error {
	if injection == nil || injection.value == nil || injection.value.IsZero() {
		return &InjectionError{Message: "not an injection a credential provider sealed"}
	}
	switch injection.target.Kind {
	case TargetFill:
		return runner.Fill(ctx, injection.target.Selector, injection.value.Reveal())
	case TargetHeader:
		return runner.SetHeader(ctx, injection.target.Name, injection.value.Reveal())
	}
	return &InjectionError{Message: fmt.Sprintf("an injection into a target of kind %q, which is neither fill nor header", injection.target.Kind)}
}

// UseCredentialInput is what UseCredential takes.
type UseCredentialInput struct {
	// Provider is the provider, as Providers.Credentials gives it.
	Provider CredentialProvider
	// ProviderName is the provider's name, which the use records.
	ProviderName string
	Ref          string
	Target       InjectionTarget
	Runner       InjectionRunner
	// Record records the use, a run record's credential log; the record
	// holds the reference and the time. Nil records nothing.
	Record func(ctx context.Context, use CredentialUse) error
	// Now is the clock the use is stamped by; nil reads the system's.
	Now func() time.Time
}

// UseCredential resolves use.Ref into use.Target through the provider,
// applies it in the runner and records the use. The caller gets the use,
// which holds no value. An injection for another reference or another
// target is refused before the runner sees it.
func UseCredential(ctx context.Context, use UseCredentialInput) (CredentialUse, error) {
	injection, err := use.Provider.Resolve(ctx, use.Ref, use.Target)
	if err != nil {
		return CredentialUse{}, err
	}
	if injection == nil || injection.value == nil || injection.value.IsZero() {
		return CredentialUse{}, &InjectionError{Message: fmt.Sprintf("credential provider %s answered %s with no sealed injection", use.ProviderName, use.Ref)}
	}
	if injection.ref != use.Ref || injection.target != use.Target.normal() {
		return CredentialUse{}, &InjectionError{Message: fmt.Sprintf("credential provider %s answered %s with an injection for %s into another target", use.ProviderName, use.Ref, injection.ref)}
	}
	if err := ApplyInjection(ctx, injection, use.Runner); err != nil {
		return CredentialUse{}, err
	}
	now := use.Now
	if now == nil {
		now = time.Now
	}
	record := CredentialUse{Ref: use.Ref, Provider: use.ProviderName, Target: injection.target, UsedAt: now()}
	if use.Record != nil {
		if err := use.Record(ctx, record); err != nil {
			return CredentialUse{}, err
		}
	}
	return record, nil
}
