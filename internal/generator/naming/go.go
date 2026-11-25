package naming

import "fmt"

// GoConfig is the [go] table of superschematic.toml: shapes of the
// generated Go types and ORM beyond the core's. Its defaults are the
// core's output. A project whose hand-written Go code another compiler's
// output shaped sets it, so that code compiles and behaves the same
// against the core's output.
type GoConfig struct {
	// LimitConstants is "off" (default) or "on". "on" writes, beside each
	// type, an untyped constant for each limit a field declares with
	// Validate<T, {...}>, at the bound Validate checks: the field's limit
	// merged with its scalar's (D160). A module that aliases a
	// dependency's type aliases its constants too.
	LimitConstants string `toml:"limit_constants"`
}

// The values of the [go] keys.
const (
	GoOn  = "on"
	GoOff = "off"
)

func defaultGoConfig() GoConfig {
	return GoConfig{
		LimitConstants: GoOff,
	}
}

// fill gives each empty key its default.
func (c *GoConfig) fill(d GoConfig, fill func(dst *string, def string)) {
	fill(&c.LimitConstants, d.LimitConstants)
}

// check refuses a value the [go] table does not take.
func (c GoConfig) check() error {
	for _, kv := range [][2]string{{"limit_constants", c.LimitConstants}} {
		if kv[1] != "" && kv[1] != GoOn && kv[1] != GoOff {
			return fmt.Errorf("go.%s is %q: use %q or %q", kv[0], kv[1], GoOn, GoOff)
		}
	}
	return nil
}

// WritesLimitConstants reports whether limit_constants is "on".
func (c GoConfig) WritesLimitConstants() bool { return c.LimitConstants == GoOn }
