package ir

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// DocsLifecycle is where an operation is in its supported lifecycle.
type DocsLifecycle string

const (
	DocsLifecycleDraft        DocsLifecycle = "draft"
	DocsLifecycleExperimental DocsLifecycle = "experimental"
	DocsLifecycleActive       DocsLifecycle = "active"
	DocsLifecycleDeprecated   DocsLifecycle = "deprecated"
	DocsLifecycleRetired      DocsLifecycle = "retired"
)

// DocsVisibility is how an operation appears in generated documentation. It
// does not grant or restrict access to the operation.
type DocsVisibility string

const (
	DocsVisibilityPublic   DocsVisibility = "public"
	DocsVisibilityInternal DocsVisibility = "internal"
	DocsVisibilityPreview  DocsVisibility = "preview"
)

// DocsAudience names the primary reader of an operation. The core accepts
// any value; an extension with a closed set of audiences enforces it with a
// check it registers on the registry.
type DocsAudience string

// DocsMappingStatus records how certain the operation's capability is.
type DocsMappingStatus string

const (
	DocsMappingStatusMapped    DocsMappingStatus = "mapped"
	DocsMappingStatusUncertain DocsMappingStatus = "uncertain"
)

// DocsReplayMode is the guarantee an operation gives a caller that sends the
// same call twice. It is declared, never inferred from the operation's name
// or arguments.
type DocsReplayMode string

const (
	// DocsReplayModeReadOnly: the operation changes nothing; a repeat is
	// safe.
	DocsReplayModeReadOnly DocsReplayMode = "read_only"
	// DocsReplayModeIdempotent: a repeat with the same idempotency keys has
	// the effect of one call.
	DocsReplayModeIdempotent DocsReplayMode = "idempotent"
	// DocsReplayModeCompareAndSwap: the call carries the revision it
	// expects, and a repeat after the first succeeds is rejected.
	DocsReplayModeCompareAndSwap DocsReplayMode = "compare_and_swap"
)

// OperationDocs is the reader-facing documentation of one operation,
// declared with @docs. OpenAPI takes the summary, description and deprecated
// flag from it and carries the whole record as a vendor extension.
type OperationDocs struct {
	// Title is the short reader-facing name: the OpenAPI summary.
	Title string `json:"title" yaml:"title"`

	// Description is the reader-facing description: the OpenAPI
	// description. It replaces the operation's comment there.
	Description string `json:"description" yaml:"description"`

	// Capability is a stable dotted identifier for what the operation does
	// ("orders.returns.create"): at least two lowercase segments.
	Capability string `json:"capability" yaml:"capability"`

	Lifecycle  DocsLifecycle  `json:"lifecycle" yaml:"lifecycle"`
	Visibility DocsVisibility `json:"visibility" yaml:"visibility"`

	// Audience is the operation's primary reader; empty when not declared.
	Audience DocsAudience `json:"audience,omitempty" yaml:"audience,omitempty"`

	// MappingStatus is "mapped" unless the author marks the capability
	// "uncertain". The TypeScript frontend fills the default.
	MappingStatus DocsMappingStatus `json:"mappingStatus" yaml:"mappingStatus"`

	// Replacement names what replaces a deprecated or retired operation.
	Replacement string `json:"replacement,omitempty" yaml:"replacement,omitempty"`

	// Sunset is the date (YYYY-MM-DD) the operation stops being served.
	Sunset string `json:"sunset,omitempty" yaml:"sunset,omitempty"`

	// ReplayMode is the operation's replay guarantee; empty declares none.
	ReplayMode DocsReplayMode `json:"replayMode,omitempty" yaml:"replayMode,omitempty"`

	// IdempotencyKeyPointers and ExpectedRevisionPointers are RFC 6901 JSON
	// pointers into the operation's generated tool-argument object (the
	// parameters of tools/schema.json): the arguments that make a repeat
	// idempotent, and the arguments that carry the expected revision. The
	// SDK generators check that each resolves to a required argument.
	IdempotencyKeyPointers   []string `json:"idempotencyKeyPointers,omitempty" yaml:"idempotencyKeyPointers,omitempty"`
	ExpectedRevisionPointers []string `json:"expectedRevisionPointers,omitempty" yaml:"expectedRevisionPointers,omitempty"`

	// UseWhen and DoNotUseWhen tell a caller, a person or a model, when to
	// choose this operation and when to choose another.
	UseWhen      string `json:"useWhen,omitempty" yaml:"useWhen,omitempty"`
	DoNotUseWhen string `json:"doNotUseWhen,omitempty" yaml:"doNotUseWhen,omitempty"`

	// Success is the outcome a caller should expect after a successful call.
	Success string `json:"success,omitempty" yaml:"success,omitempty"`

	// Errors lists the operation's expected errors and how to correct them.
	Errors []OperationDocsError `json:"errors,omitempty" yaml:"errors,omitempty"`
}

// OperationDocsError is one expected error of an operation and the usual
// correction a caller makes before retrying.
type OperationDocsError struct {
	Code             string `json:"code" yaml:"code"`
	Description      string `json:"description" yaml:"description"`
	CommonCorrection string `json:"commonCorrection" yaml:"commonCorrection"`
}

var docsCapabilityPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*(?:\.[a-z0-9]+(?:-[a-z0-9]+)*)+$`)

var docsJSONPointerPattern = regexp.MustCompile(`^(?:/(?:[^~/]|~[01])*)+$`)

// ValidateOperationDocs checks the shape of an operation's @docs record.
// nil is valid: @docs is optional. The audience is not checked against a
// value set; that is an extension's policy.
func ValidateOperationDocs(docs *OperationDocs) error {
	if docs == nil {
		return nil
	}
	for _, field := range []struct{ name, value string }{
		{"title", docs.Title},
		{"description", docs.Description},
		{"capability", docs.Capability},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s must be non-empty", field.name)
		}
	}
	if strings.TrimSpace(docs.Title) != docs.Title {
		return fmt.Errorf("title must not contain surrounding whitespace")
	}
	if !docsCapabilityPattern.MatchString(docs.Capability) {
		return fmt.Errorf("capability %q must contain at least two dot-separated lowercase segments", docs.Capability)
	}

	switch docs.Lifecycle {
	case DocsLifecycleDraft, DocsLifecycleExperimental, DocsLifecycleActive,
		DocsLifecycleDeprecated, DocsLifecycleRetired:
	default:
		return fmt.Errorf("lifecycle %q must be draft, experimental, active, deprecated, or retired", docs.Lifecycle)
	}
	switch docs.Visibility {
	case DocsVisibilityPublic, DocsVisibilityInternal, DocsVisibilityPreview:
	default:
		return fmt.Errorf("visibility %q must be public, internal, or preview", docs.Visibility)
	}
	switch docs.MappingStatus {
	case DocsMappingStatusMapped, DocsMappingStatusUncertain:
	default:
		return fmt.Errorf("mappingStatus %q must be mapped or uncertain", docs.MappingStatus)
	}

	if docs.Replacement != "" && strings.TrimSpace(docs.Replacement) == "" {
		return fmt.Errorf("replacement must be non-empty when provided")
	}
	if docs.Sunset != "" {
		if strings.TrimSpace(docs.Sunset) != docs.Sunset {
			return fmt.Errorf("sunset must not contain surrounding whitespace")
		}
		if _, err := time.Parse("2006-01-02", docs.Sunset); err != nil {
			return fmt.Errorf("sunset %q must use YYYY-MM-DD", docs.Sunset)
		}
	}
	for _, field := range []struct{ name, value string }{
		{"audience", string(docs.Audience)},
		{"useWhen", docs.UseWhen},
		{"doNotUseWhen", docs.DoNotUseWhen},
		{"success", docs.Success},
	} {
		if err := checkOptionalText(field.name, field.value); err != nil {
			return err
		}
	}
	if err := validateDocsErrors(docs.Errors); err != nil {
		return err
	}
	return validateDocsReplay(docs)
}

// validateDocsReplay checks the replay mode and its pointers: every pointer
// is an RFC 6901 pointer, listed once; idempotent needs idempotency keys and
// no revision; compare_and_swap needs a revision; read_only and no mode take
// no pointers. Whether a pointer resolves is checked against the generated
// tool arguments, by the SDK generators.
func validateDocsReplay(docs *OperationDocs) error {
	for _, field := range []struct {
		name     string
		pointers []string
	}{
		{"idempotencyKeyPointers", docs.IdempotencyKeyPointers},
		{"expectedRevisionPointers", docs.ExpectedRevisionPointers},
	} {
		seen := make(map[string]struct{}, len(field.pointers))
		for _, pointer := range field.pointers {
			if !docsJSONPointerPattern.MatchString(pointer) {
				return fmt.Errorf("%s value %q must be an RFC 6901 JSON pointer", field.name, pointer)
			}
			if _, duplicate := seen[pointer]; duplicate {
				return fmt.Errorf("%s value %q must not be duplicated", field.name, pointer)
			}
			seen[pointer] = struct{}{}
		}
	}
	keys, revisions := len(docs.IdempotencyKeyPointers) > 0, len(docs.ExpectedRevisionPointers) > 0
	switch docs.ReplayMode {
	case "":
		if keys || revisions {
			return fmt.Errorf("replay pointers require replayMode")
		}
	case DocsReplayModeReadOnly:
		if keys || revisions {
			return fmt.Errorf("read_only replayMode cannot declare replay pointers")
		}
	case DocsReplayModeIdempotent:
		if !keys {
			return fmt.Errorf("idempotent replayMode requires idempotencyKeyPointers")
		}
		if revisions {
			return fmt.Errorf("idempotent replayMode cannot declare expectedRevisionPointers")
		}
	case DocsReplayModeCompareAndSwap:
		if !revisions {
			return fmt.Errorf("compare_and_swap replayMode requires expectedRevisionPointers")
		}
	default:
		return fmt.Errorf("replayMode %q must be read_only, idempotent, or compare_and_swap", docs.ReplayMode)
	}
	return nil
}

// validateDocsErrors requires every field of every expected error, with no
// surrounding whitespace, and a code that no other entry repeats, ignoring
// case.
func validateDocsErrors(docErrors []OperationDocsError) error {
	seen := make(map[string]struct{}, len(docErrors))
	for _, docError := range docErrors {
		for _, field := range []struct{ name, value string }{
			{"errors code", docError.Code},
			{"errors description", docError.Description},
			{"errors commonCorrection", docError.CommonCorrection},
		} {
			trimmed := strings.TrimSpace(field.value)
			if trimmed == "" {
				return fmt.Errorf("%s must be non-empty", field.name)
			}
			if trimmed != field.value {
				return fmt.Errorf("%s must not contain surrounding whitespace", field.name)
			}
		}
		code := strings.ToLower(docError.Code)
		if _, duplicate := seen[code]; duplicate {
			return fmt.Errorf("errors code %q must not be duplicated", docError.Code)
		}
		seen[code] = struct{}{}
	}
	return nil
}

// checkOptionalText accepts an absent value and rejects a blank or padded
// one.
func checkOptionalText(name, value string) error {
	if value == "" {
		return nil
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("%s must be non-empty when provided", name)
	}
	if trimmed != value {
		return fmt.Errorf("%s must not contain surrounding whitespace", name)
	}
	return nil
}

// validateDocs checks the documentation metadata of every field and
// operation: each operation's @docs, @mcp and @icon, that @docs and @mcp
// records sit on operations only, that a visible @mcp tool also has @docs,
// and that a field's title, purpose and icon are not blank. It runs for
// every authoring form, so a data-form file is held to what the TypeScript
// decorators enforce.
func (s *Schema) validateDocs() []error {
	var errs []error
	fields := func(owner string, list []*FieldDef) {
		for _, f := range list {
			if f == nil {
				continue
			}
			if f.Docs != nil {
				errs = append(errs, fmt.Errorf("%s.%s carries operation docs; only an operation takes @docs", owner, f.Name))
			}
			if f.MCP != nil {
				errs = append(errs, fmt.Errorf("%s.%s carries an MCP record; only an operation takes @mcp", owner, f.Name))
			}
			for _, text := range []struct{ name, value string }{
				{"title", f.Title},
				{"purpose", f.Purpose},
				{"icon", f.Icon},
			} {
				if text.value != "" && strings.TrimSpace(text.value) == "" {
					errs = append(errs, fmt.Errorf("%s.%s %s must be non-empty when provided", owner, f.Name, text.name))
				}
			}
		}
	}
	for _, name := range sortedStringMapKeys(s.Types) {
		if td := s.Types[name]; td != nil {
			fields(td.Name, td.Fields)
			if td.TraitConfig != nil {
				fields(td.Name+" trait config", td.TraitConfig.Fields)
			}
		}
	}
	for _, name := range sortedStringMapKeys(s.Inputs) {
		if td := s.Inputs[name]; td != nil {
			fields(td.Name, td.Fields)
		}
	}
	for _, set := range s.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op == nil {
				continue
			}
			if err := ValidateOperationDocs(op.Docs); err != nil {
				errs = append(errs, fmt.Errorf("%s.%s has invalid docs: %w", set.Name, op.Name, err))
			}
			if err := ValidateOperationMCP(op.MCP); err != nil {
				errs = append(errs, fmt.Errorf("%s.%s has an invalid @mcp: %w", set.Name, op.Name, err))
			}
			if op.MCP != nil && !op.MCP.Hidden && op.Docs == nil {
				errs = append(errs, fmt.Errorf("%s.%s is a visible @mcp tool and must also declare @docs", set.Name, op.Name))
			}
			if op.Icon != "" {
				if err := ValidateOperationIcon(op.Icon); err != nil {
					errs = append(errs, fmt.Errorf("%s.%s has an invalid @icon: %w", set.Name, op.Name, err))
				}
			}
		}
	}
	return errs
}
