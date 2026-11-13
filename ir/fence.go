package ir

import (
	"fmt"
)

// Fence is a `@fenced` declaration of a DB table (D118): an integer field
// of the table, the fence, that every update the fence covers sets to the
// stored value plus one. A writer that compares the fence in its WHERE
// clause then knows no covered write came between its read and its write.
//
// A trigger refuses a covered update that does not advance the fence, and
// any update that skips a value, moves it back or sets it null; the
// generated repositories advance it themselves. An insert is not fenced.
//
// In TypeScript: @fenced<T>({ field, covers? })
type Fence struct {
	// Field is the fence: a required field of the table whose column is
	// SMALLINT, INTEGER or BIGINT.
	Field string `json:"field" yaml:"field"`

	// Covers are the fields of the table whose change the fence counts, in
	// the order written; a to-one relation is its key's column. Empty, the
	// fence counts every update of the row, and the table has no other
	// fence. With them, an update that changes none of them keeps the
	// fence or adds one.
	Covers []string `json:"covers,omitempty" yaml:"covers,omitempty" jsonschema:"minItems=1"`
}

// CoversRow reports whether the fence counts every update of the row: one
// declared without covers.
func (f *Fence) CoversRow() bool {
	return f != nil && len(f.Covers) == 0
}

// FenceOf returns td's fence on the field named field, or nil when the
// type declares none on it.
func (td *TypeDef) FenceOf(field string) *Fence {
	if td == nil {
		return nil
	}
	for _, f := range td.Fences {
		if f != nil && f.Field == field {
			return f
		}
	}
	return nil
}

// validateFences checks td's @fenced declarations (D118) for what the
// declarations alone decide, in every authoring form: one fence per field,
// none on a field a @stateMachine governs, covers on every fence of a type
// with several, no empty covers, and no covered field that is listed
// twice, is a fence, or is another fence's to cover. Each error names the
// type and the fence's field. What needs the fields, the table and the
// service (that the type is a table a repository writes, that the fence
// is a required integer column, and that each covered field stores a
// column) is the loader's verification pass's to check.
func (s *Schema) validateFences(td *TypeDef) []error {
	var errs []error
	fences := map[string]bool{}
	for _, fence := range td.Fences {
		if fence != nil {
			fences[fence.Field] = true
		}
	}
	seen := map[string]bool{}
	// coveredBy is the fence that covers a field, by the field's name.
	coveredBy := map[string]string{}
	for i, fence := range td.Fences {
		switch {
		case fence == nil:
			errs = append(errs, fmt.Errorf("@fenced %d on %s is null; write { field }, or { field, covers }", i, td.Name))
			continue
		case fence.Field == "":
			errs = append(errs, fmt.Errorf("@fenced %d on %s names no field; name the integer field every covered write advances", i, td.Name))
			continue
		case seen[fence.Field]:
			errs = append(errs, fmt.Errorf("@fenced on %s.%s: %s declares two fences on %s; a field is one fence, so list what it covers in one declaration", td.Name, fence.Field, td.Name, fence.Field))
			continue
		}
		seen[fence.Field] = true
		fail := func(format string, args ...any) {
			errs = append(errs, fmt.Errorf("@fenced on %s.%s: %s", td.Name, fence.Field, fmt.Sprintf(format, args...)))
		}
		if td.StateMachineOf(fence.Field) != nil {
			fail("%s is the field a @stateMachine of %s governs, which holds a state and no count; name an integer field of its own", fence.Field, td.Name)
		}
		switch {
		// An empty covers counts nothing, and the IR's JSON leaves it out,
		// so a schema read back would hold a fence that counts every
		// update.
		case fence.Covers != nil && len(fence.Covers) == 0:
			fail("covers is empty, so the fence would count no write; list the fields whose change it counts, or leave covers out and it counts every update of the row")
		case len(fence.Covers) == 0 && len(td.Fences) > 1:
			fail("%s has more than one fence, and this one lists no covers, so every update would advance it, a write of another fence's fields among them; list in covers the fields each fence counts", td.Name)
		}
		listed := map[string]bool{}
		for _, name := range fence.Covers {
			switch {
			case name == "":
				fail("covers names an empty field; name a field of %s", td.Name)
			case listed[name]:
				fail("covers lists %s twice; list each field once", name)
			case name == fence.Field:
				fail("covers lists %s, the fence itself, which a covered write advances; leave it out", name)
			case fences[name]:
				fail("covers lists %s, the field of another fence of %s, which that fence's writes advance; leave it out", name, td.Name)
			case coveredBy[name] != "":
				fail("covers lists %s, which the fence on %s.%s covers; one fence covers a field, so a write of it advances one counter", name, td.Name, coveredBy[name])
			default:
				coveredBy[name] = fence.Field
			}
			listed[name] = true
		}
	}
	return errs
}
