package parse

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// coerceInt converts JSON-decoded numerics into int64 for Int-primitive
// scalars. Returns the coerced value and true on success.
//
// Lenient mode also accepts numeric-looking strings; strict mode does not.
// This mirrors what callers trip over when YAML or env vars round-trip
// through string.
func coerceInt(value any, strict bool) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case int32:
		return int64(v), true
	case float64:
		return integerFloat64(v)
	case float32:
		return integerFloat64(float64(v))
	case json.Number:
		i, err := v.Int64()
		if err == nil {
			return i, true
		}
		return 0, false
	case string:
		if strict {
			return 0, false
		}
		i, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err == nil {
			return i, true
		}
		return 0, false
	default:
		return 0, false
	}
}

func integerFloat64(value float64) (int64, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value ||
		value < math.MinInt64 || value >= float64(math.MaxInt64) {
		return 0, false
	}
	return int64(value), true
}

// coerceFloat converts JSON-decoded numerics into float64 for Float-primitive
// scalars. Mirrors coerceInt's lenient/strict semantics.
func coerceFloat(value any, strict bool) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case int32:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		if err == nil {
			return f, true
		}
		return 0, false
	case string:
		if strict {
			return 0, false
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err == nil {
			return f, true
		}
		return 0, false
	default:
		return 0, false
	}
}

// coerceBool converts string "true"/"false" (case-insensitive) into bool in
// lenient mode. Strict mode rejects strings.
func coerceBool(value any, strict bool) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		if strict {
			return false, false
		}
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
		return false, false
	default:
		return false, false
	}
}
