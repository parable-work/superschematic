package canonical

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	integerPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)
	numberPattern  = regexp.MustCompile(`^(-?)(0|[1-9][0-9]*)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?$`)
)

// integerRule keeps an integer's digits exactly, however wide: an optional
// minus and no leading zeros, with -0 written 0. A fraction or an exponent is
// refused.
func integerRule(value any) (string, error) {
	n, ok := value.(json.Number)
	if !ok || !integerPattern.MatchString(n.String()) {
		return "", fmt.Errorf("%s is not an integer", describe(value))
	}
	if n.String() == "-0" {
		return "0", nil
	}
	return n.String(), nil
}

// numberRule writes a number's exact decimal value in the layout
// ECMAScript's Number::toString uses: plain digits while the decimal point
// falls within 21 digits of the first and no more than 6 zeros follow it,
// else one digit, a fraction and an exponent with its sign (1e+21, 1.5e-7).
// For a value a double holds with its shortest digits, that is the text
// JSON.stringify and Go's encoding/json write. Trailing zeros go (1.50 is
// 1.5), and -0 is 0; the digits are never rounded.
func numberRule(value any) (string, error) {
	n, ok := value.(json.Number)
	if !ok {
		return "", fmt.Errorf("%s is not a number", describe(value))
	}
	m := numberPattern.FindStringSubmatch(n.String())
	if m == nil {
		return "", fmt.Errorf("%q is not a JSON number", n.String())
	}
	negative, whole, fraction, exponentText := m[1] == "-", m[2], m[3], m[4]
	exponent := int64(0)
	if exponentText != "" {
		e, err := strconv.ParseInt(exponentText, 10, 32)
		if err != nil {
			return "", fmt.Errorf("the exponent of %s is out of range", n.String())
		}
		exponent = e
	}
	// The value is 0.digits * 10^point.
	digits := whole + fraction
	point := int64(len(whole)) + exponent
	trimmed := strings.TrimLeft(digits, "0")
	point -= int64(len(digits) - len(trimmed))
	digits = strings.TrimRight(trimmed, "0")
	if digits == "" {
		return "0", nil
	}
	sign := ""
	if negative {
		sign = "-"
	}
	k := int64(len(digits))
	switch {
	case k <= point && point <= 21:
		return sign + digits + strings.Repeat("0", int(point-k)), nil
	case 0 < point && point <= 21:
		return sign + digits[:point] + "." + digits[point:], nil
	case -6 < point && point <= 0:
		return sign + "0." + strings.Repeat("0", int(-point)) + digits, nil
	}
	e := point - 1
	exponentSign := "+"
	if e < 0 {
		exponentSign = "-"
		e = -e
	}
	mantissa := digits[:1]
	if k > 1 {
		mantissa += "." + digits[1:]
	}
	return sign + mantissa + "e" + exponentSign + strconv.FormatInt(e, 10), nil
}
