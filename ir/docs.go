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
}

var docsCapabilityPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*(?:\.[a-z0-9]+(?:-[a-z0-9]+)*)+$`)

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
	return checkOptionalText("audience", string(docs.Audience))
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
// operation: each operation's @docs record, and that @docs records sit on
// operations only. It runs for every authoring form, so a data-form file is
// held to what the TypeScript decorator enforces.
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
		}
	}
	return errs
}
