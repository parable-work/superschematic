package sqlmigrate

import (
	"regexp"
	"strconv"
	"strings"
)

// pgType is a Postgres column type split into its base name, its
// modifiers and whether it is an array.
type pgType struct {
	base  string // "VARCHAR", "NUMERIC", "TEXT"
	mods  []int  // VARCHAR(80) -> [80]; NUMERIC(10,2) -> [10 2]
	array bool
}

var pgTypeAliases = map[string]string{
	"INT":                         "INTEGER",
	"INT4":                        "INTEGER",
	"INT2":                        "SMALLINT",
	"INT8":                        "BIGINT",
	"FLOAT8":                      "DOUBLE PRECISION",
	"FLOAT4":                      "REAL",
	"DECIMAL":                     "NUMERIC",
	"BOOL":                        "BOOLEAN",
	"CHARACTER VARYING":           "VARCHAR",
	"TIMESTAMP WITH TIME ZONE":    "TIMESTAMPTZ",
	"TIMESTAMP WITHOUT TIME ZONE": "TIMESTAMP",
}

var pgTypePattern = regexp.MustCompile(`^([A-Z][A-Z0-9_ ]*?)\s*(?:\(([0-9,\s]*)\))?((?:\[\])*)$`)

// parsePGType reads a type as the model spells it. A type it cannot read
// keeps its whole spelling as its base, so it converts only to itself.
func parsePGType(s string) pgType {
	norm := strings.ToUpper(strings.Join(strings.Fields(s), " "))
	m := pgTypePattern.FindStringSubmatch(norm)
	if m == nil {
		return pgType{base: norm}
	}
	t := pgType{base: strings.TrimSpace(m[1]), array: m[3] != ""}
	if alias, ok := pgTypeAliases[t.base]; ok {
		t.base = alias
	}
	if m[2] != "" {
		for _, part := range strings.Split(m[2], ",") {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil {
				return pgType{base: norm}
			}
			t.mods = append(t.mods, n)
		}
	}
	return t
}

func (t pgType) equal(o pgType) bool {
	if t.base != o.base || t.array != o.array || len(t.mods) != len(o.mods) {
		return false
	}
	for i := range t.mods {
		if t.mods[i] != o.mods[i] {
			return false
		}
	}
	return true
}

func isPGText(base string) bool {
	return base == "TEXT" || base == "VARCHAR" || base == "CITEXT" || base == "CHAR"
}

// pgIntegerBytes is the width of each integer type.
var pgIntegerBytes = map[string]int{"SMALLINT": 2, "INTEGER": 4, "BIGINT": 8}

// pgExactFloat lists the integer types a float holds every value of.
var pgExactFloat = map[string]map[string]bool{
	"REAL":             {"SMALLINT": true},
	"DOUBLE PRECISION": {"SMALLINT": true, "INTEGER": true},
}

func isPGFloat(base string) bool { return base == "REAL" || base == "DOUBLE PRECISION" }

// convert classifies ALTER COLUMN ... TYPE from one column's type to
// another's (pgConvert).
func (postgresDialect) convert(from, to *Column) conversion {
	return pgConvert(from.Type, to.Type)
}

// pgConvert classifies ALTER COLUMN ... TYPE from one type to another. A
// cast the plan writes is explicit (USING col::type), so a value too long
// for a VARCHAR is cut, not refused, and a value outside a number type's
// range is refused.
func pgConvert(from, to string) conversion {
	f, t := parsePGType(from), parsePGType(to)
	if f.equal(t) {
		return conversion{kind: convertSame}
	}
	if f.array != t.array {
		return conversion{kind: convertImpossible}
	}
	c := convertScalar(f, t)
	// Postgres converts an array element by element; treat a change no
	// element needs a rewrite for as one anyway.
	if f.array && c.kind == convertCoercible {
		c.kind = convertRewrite
	}
	return c
}

func convertScalar(f, t pgType) conversion {
	switch {
	case f.base == t.base && f.base == "VARCHAR":
		switch {
		case len(t.mods) == 0 || len(f.mods) > 0 && t.mods[0] >= f.mods[0]:
			return conversion{kind: convertCoercible}
		default:
			return conversion{kind: convertRewrite, lossy: true}
		}
	case f.base == t.base && f.base == "NUMERIC":
		switch {
		case len(t.mods) == 0:
			return conversion{kind: convertCoercible}
		case len(f.mods) == 0:
			return conversion{kind: convertMayFail, lossy: true}
		}
		fScale, tScale := scaleOf(f), scaleOf(t)
		switch {
		case fScale == tScale && t.mods[0] >= f.mods[0]:
			return conversion{kind: convertCoercible}
		case tScale < fScale:
			return conversion{kind: convertMayFail, lossy: true}
		default:
			return conversion{kind: convertMayFail}
		}
	case f.base == t.base:
		// Another type whose modifier changes (CHAR(n), TIME(p)): rewrite,
		// and assume the narrower one loses data.
		return conversion{kind: convertRewrite, lossy: true}
	case isPGText(f.base) && isPGText(t.base):
		switch {
		case f.base == "VARCHAR" && t.base == "TEXT":
			return conversion{kind: convertCoercible}
		case t.base == "VARCHAR" && len(t.mods) > 0, t.base == "CHAR":
			return conversion{kind: convertRewrite, lossy: true}
		default:
			return conversion{kind: convertRewrite}
		}
	case isPGText(t.base):
		// Every type has a text form.
		if len(t.mods) > 0 {
			return conversion{kind: convertRewrite, lossy: true}
		}
		return conversion{kind: convertRewrite}
	case isPGText(f.base):
		// Text parses as any type, and fails on what does not parse.
		return conversion{kind: convertMayFail}
	}

	fInt, tInt := pgIntegerBytes[f.base], pgIntegerBytes[t.base]
	switch {
	case fInt > 0 && tInt > 0:
		if tInt > fInt {
			return conversion{kind: convertRewrite}
		}
		return conversion{kind: convertMayFail}
	case fInt > 0 && t.base == "NUMERIC":
		if len(t.mods) == 0 {
			return conversion{kind: convertRewrite}
		}
		return conversion{kind: convertMayFail}
	case fInt > 0 && isPGFloat(t.base):
		return conversion{kind: convertRewrite, lossy: !pgExactFloat[t.base][f.base]}
	case (isPGFloat(f.base) || f.base == "NUMERIC") && tInt > 0:
		return conversion{kind: convertMayFail, lossy: true}
	case f.base == "REAL" && t.base == "DOUBLE PRECISION":
		return conversion{kind: convertRewrite}
	case f.base == "DOUBLE PRECISION" && t.base == "REAL":
		return conversion{kind: convertMayFail, lossy: true}
	case isPGFloat(f.base) && t.base == "NUMERIC":
		return conversion{kind: convertMayFail}
	case f.base == "NUMERIC" && isPGFloat(t.base):
		return conversion{kind: convertMayFail, lossy: true}
	}

	switch f.base + ">" + t.base {
	case "DATE>TIMESTAMPTZ", "DATE>TIMESTAMP", "TIMESTAMP>TIMESTAMPTZ", "TIMESTAMPTZ>TIMESTAMP", "JSONB>JSON":
		return conversion{kind: convertRewrite}
	case "TIMESTAMPTZ>DATE", "TIMESTAMP>DATE", "TIMESTAMPTZ>TIME", "TIMESTAMP>TIME", "JSON>JSONB":
		return conversion{kind: convertRewrite, lossy: true}
	case "BOOLEAN>INTEGER":
		return conversion{kind: convertRewrite}
	case "INTEGER>BOOLEAN":
		return conversion{kind: convertRewrite, lossy: true}
	case "JSONB>BOOLEAN", "JSONB>NUMERIC", "JSONB>SMALLINT", "JSONB>INTEGER", "JSONB>BIGINT", "JSONB>REAL", "JSONB>DOUBLE PRECISION":
		return conversion{kind: convertMayFail}
	}
	return conversion{kind: convertImpossible}
}

// scaleOf is a NUMERIC's scale: 0 when it gives only a precision.
func scaleOf(t pgType) int {
	if len(t.mods) > 1 {
		return t.mods[1]
	}
	return 0
}
