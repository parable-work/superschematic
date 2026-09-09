package parse

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCoerceInt_Lenient(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want int64
		ok   bool
	}{
		{"int64", int64(42), 42, true},
		{"int", int(42), 42, true},
		{"int32", int32(42), 42, true},
		{"float64", float64(42), 42, true},
		{"float64 fraction rejected", float64(42.9), 0, false},
		{"json.Number int", json.Number("42"), 42, true},
		{"json.Number float rejected", json.Number("42.5"), 0, false},
		{"string int", "42", 42, true},
		{"string float rejected", "42.5", 0, false},
		{"string trimmed", "  42  ", 42, true},
		{"string bad", "foo", 0, false},
		{"bool rejected", true, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := coerceInt(tc.in, false)
			assert.Equal(t, tc.ok, ok)
			if ok {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestCoerceInt_Strict(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want int64
		ok   bool
	}{
		{"int64", int64(42), 42, true},
		{"float64", float64(42), 42, true},
		{"string rejected", "42", 0, false},
		{"json.Number ok", json.Number("42"), 42, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := coerceInt(tc.in, true)
			assert.Equal(t, tc.ok, ok)
			if ok {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestCoerceFloat(t *testing.T) {
	got, ok := coerceFloat(float64(1.5), false)
	assert.True(t, ok)
	assert.InDelta(t, 1.5, got, 1e-9)

	got, ok = coerceFloat(int64(3), false)
	assert.True(t, ok)
	assert.InDelta(t, 3.0, got, 1e-9)

	got, ok = coerceFloat("1.5", false)
	assert.True(t, ok)
	assert.InDelta(t, 1.5, got, 1e-9)

	_, ok = coerceFloat("1.5", true)
	assert.False(t, ok, "strict mode rejects string")

	_, ok = coerceFloat(true, false)
	assert.False(t, ok)
}

func TestCoerceBool(t *testing.T) {
	cases := []struct {
		in     any
		strict bool
		want   bool
		ok     bool
	}{
		{true, false, true, true},
		{false, false, false, true},
		{"true", false, true, true},
		{"FALSE", false, false, true},
		{"  true  ", false, true, true},
		{"true", true, false, false},
		{"yes", false, false, false},
		{1, false, false, false},
	}
	for _, tc := range cases {
		got, ok := coerceBool(tc.in, tc.strict)
		assert.Equal(t, tc.ok, ok, "input=%v strict=%v", tc.in, tc.strict)
		if ok {
			assert.Equal(t, tc.want, got)
		}
	}
}
