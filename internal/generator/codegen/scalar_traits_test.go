package codegen

import (
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

func TestContainsTypeSegment(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
		term     string
		want     bool
	}{
		{name: "exact match", typeName: "uuid", term: "uuid", want: true},
		{name: "dot-qualified package", typeName: "uuid.uuid", term: "uuid", want: true},
		{name: "full Go import path", typeName: "github.com/google/uuid.uuid", term: "uuid", want: true},
		{name: "pointer type", typeName: "*uuid.uuid", term: "uuid", want: true},
		{name: "substring should not match", typeName: "non_uuid_helper", term: "uuid", want: false},
		{name: "prefix should not match", typeName: "uuidgen", term: "uuid", want: false},
		{name: "suffix should not match", typeName: "myuuid", term: "uuid", want: false},
		{name: "embedded should not match", typeName: "nouuidhere", term: "uuid", want: false},
		{name: "duration exact", typeName: "duration", term: "duration", want: true},
		{name: "time.duration", typeName: "time.duration", term: "duration", want: true},
		{name: "duration substring no match", typeName: "nondurationthing", term: "duration", want: false},
		{name: "empty type name", typeName: "", term: "uuid", want: false},
		{name: "empty term", typeName: "uuid", term: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsTypeSegment(tt.typeName, tt.term)
			if got != tt.want {
				t.Errorf("containsTypeSegment(%q, %q) = %v, want %v", tt.typeName, tt.term, got, tt.want)
			}
		})
	}
}

func TestApplyTypeStringTraits_UUIDFalsePositive(t *testing.T) {
	traits := ScalarTraits{}
	applyTypeStringTraits(&traits, "non_uuid_helper")
	if traits.IsUUIDLike {
		t.Error("non_uuid_helper should not set IsUUIDLike")
	}
}

func TestApplyTypeStringTraits_UUIDLegitimate(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
	}{
		{name: "bare uuid", typeName: "uuid"},
		{name: "uuid.UUID", typeName: "uuid.UUID"},
		{name: "pointer", typeName: "*uuid.UUID"},
		{name: "full import path", typeName: "github.com/google/uuid.UUID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			traits := ScalarTraits{}
			applyTypeStringTraits(&traits, tt.typeName)
			if !traits.IsUUIDLike {
				t.Errorf("applyTypeStringTraits(%q) should set IsUUIDLike", tt.typeName)
			}
		})
	}
}

func TestApplyTypeStringTraits_DateTimeLegitimate(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
	}{
		{name: "go time", typeName: "time.Time"},
		{name: "python datetime", typeName: "datetime.datetime"},
		{name: "bare python datetime", typeName: "datetime"},
		{name: "typescript date", typeName: "JSDate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			traits := ScalarTraits{}
			applyTypeStringTraits(&traits, tt.typeName)
			if !traits.IsDateTimeLike {
				t.Errorf("applyTypeStringTraits(%q) should set IsDateTimeLike", tt.typeName)
			}
		})
	}
}

func TestApplyTypeStringTraits_DateTimeFalsePositive(t *testing.T) {
	traits := ScalarTraits{}
	applyTypeStringTraits(&traits, "not_datetime_helper")
	if traits.IsDateTimeLike {
		t.Error("not_datetime_helper should not set IsDateTimeLike")
	}
}

func TestBuildScalarTraits_Int64NameIsIntegerLike(t *testing.T) {
	tokens := BuildScalarTokens("Generic.Int64")
	traits := BuildScalarTraits(&ir.ScalarDef{
		Name:              "Generic.Int64",
		LanguagePrimitive: ir.LanguageNumber,
	}, tokens, "")

	if !traits.IsIntegerLike {
		t.Error("Generic.Int64 should be integer-like")
	}
}

func TestApplyTypeStringTraits_DurationFalsePositive(t *testing.T) {
	traits := ScalarTraits{}
	applyTypeStringTraits(&traits, "nondurationthing")
	if traits.IsDurationLike {
		t.Error("nondurationthing should not set IsDurationLike")
	}
}
