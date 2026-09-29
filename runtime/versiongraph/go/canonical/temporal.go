package canonical

import (
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	dateTimePattern = regexp.MustCompile(`^([0-9]{4})-([0-9]{2})-([0-9]{2})[T ]([0-9]{2}):([0-9]{2}):([0-9]{2})(?:\.([0-9]{1,9}))?(Z|[+-][0-9]{2}(?::[0-9]{2}(?::[0-9]{2})?)?)$`)
	datePattern     = regexp.MustCompile(`^([0-9]{4})-([0-9]{2})-([0-9]{2})$`)
	timePattern     = regexp.MustCompile(`^([0-9]{2}):([0-9]{2})(?::([0-9]{2})(?:\.([0-9]{1,9}))?)?$`)
	clockPattern    = regexp.MustCompile(`^([0-9]{1,2}):([0-9]{2})(?::([0-9]{2}))?\s?([AaPp])[Mm]$`)
	hyphenatedUUID  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	base62UUID      = regexp.MustCompile(`^[0-9A-Za-z]{1,22}$`)
	intervalTime    = regexp.MustCompile(`^([+-]?)([0-9]+):([0-9]{2}):([0-9]{2})(?:\.([0-9]{1,9}))?$`)
	intervalCount   = regexp.MustCompile(`^[+-]?[0-9]+$`)
)

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// uuidRule writes a UUID in the scalar core's canonical form, base62 of its
// 128 bits (the nil UUID is "0"). It reads the hyphenated form Postgres
// renders, in either case, and the base62 form.
func uuidRule(value any) (string, error) {
	s, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string", describe(value))
	}
	n := new(big.Int)
	switch {
	case hyphenatedUUID.MatchString(s):
		n.SetString(strings.ReplaceAll(s, "-", ""), 16)
	case base62UUID.MatchString(s):
		for _, c := range []byte(s) {
			n.Mul(n, big.NewInt(62))
			n.Add(n, big.NewInt(int64(strings.IndexByte(base62Alphabet, c))))
		}
		if n.BitLen() > 128 {
			return "", fmt.Errorf("%q is wider than a UUID", s)
		}
	default:
		return "", fmt.Errorf("%q is not a UUID", s)
	}
	if n.Sign() == 0 {
		return `"0"`, nil
	}
	var out []byte
	base, digit := big.NewInt(62), new(big.Int)
	for n.Sign() > 0 {
		n.DivMod(n, base, digit)
		out = append(out, base62Alphabet[digit.Int64()])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return `"` + string(out) + `"`, nil
}

// dateTimeRule writes an instant as RFC 3339 in UTC with a Z, its fraction
// of a second without trailing zeros and left out when zero: Go's
// RFC3339Nano of the UTC time. It reads the form Postgres renders, whose
// offset follows the session's time zone and may carry seconds
// (+00:17:30), and any RFC 3339 time with an offset. A year outside
// 0000-9999, BC, infinity, a time without an offset and an offset of a
// day or more are refused.
func dateTimeRule(value any) (string, error) {
	s, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string", describe(value))
	}
	m := dateTimePattern.FindStringSubmatch(s)
	if m == nil {
		return "", fmt.Errorf("%q is not a date-time with an offset", s)
	}
	offset, err := offsetSeconds(m[8])
	if err != nil {
		return "", fmt.Errorf("%q: %w", s, err)
	}
	year, month, day := atoi(m[1]), atoi(m[2]), atoi(m[3])
	hour, minute, second := atoi(m[4]), atoi(m[5]), atoi(m[6])
	nanos := atoi((m[7] + "000000000")[:9])
	t := time.Date(year, time.Month(month), day, hour, minute, second, nanos, time.FixedZone("", offset))
	if t.Year() != year || int(t.Month()) != month || t.Day() != day || t.Hour() != hour || t.Minute() != minute || t.Second() != second {
		return "", fmt.Errorf("%q is not a valid date-time", s)
	}
	utc := t.UTC()
	if utc.Year() < 0 || utc.Year() > 9999 {
		return "", fmt.Errorf("%q falls outside the years 0000-9999", s)
	}
	return `"` + utc.Format(time.RFC3339Nano) + `"`, nil
}

func offsetSeconds(text string) (int, error) {
	if text == "Z" {
		return 0, nil
	}
	sign := 1
	if text[0] == '-' {
		sign = -1
	}
	parts := strings.Split(text[1:], ":")
	seconds := atoi(parts[0]) * 3600
	if len(parts) > 1 {
		seconds += atoi(parts[1]) * 60
	}
	if len(parts) > 2 {
		seconds += atoi(parts[2])
	}
	if seconds >= 24*3600 {
		return 0, fmt.Errorf("offset %s is a day or more", text)
	}
	return sign * seconds, nil
}

// dateRule writes a calendar date as YYYY-MM-DD, the form Postgres renders.
// BC, infinity and a date that does not exist are refused.
func dateRule(value any) (string, error) {
	s, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string", describe(value))
	}
	m := datePattern.FindStringSubmatch(s)
	if m == nil {
		return "", fmt.Errorf("%q is not a date", s)
	}
	year, month, day := atoi(m[1]), atoi(m[2]), atoi(m[3])
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if t.Year() != year || int(t.Month()) != month || t.Day() != day {
		return "", fmt.Errorf("%q is not a valid date", s)
	}
	return `"` + s + `"`, nil
}

// timeRule writes a time of day as HH:MM:SS on a 24-hour clock, its
// fraction of a second without trailing zeros and left out when zero, the
// form Postgres renders. It also reads the other forms the scalar accepts,
// which a JSON value the ORM stored keeps as written: HH:MM, and a 12-hour
// clock (2:30 pm, 12:05:09AM), each as Postgres reads it into a time. The
// scalar has no fraction, but a time column written past it can hold one,
// which is kept. 24:00:00, which Postgres stores, is refused, as the scalar
// refuses it.
func timeRule(value any) (string, error) {
	s, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string", describe(value))
	}
	var hour, minute, second int
	var fraction string
	if m := timePattern.FindStringSubmatch(s); m != nil {
		hour, minute, fraction = atoi(m[1]), atoi(m[2]), m[4]
		if m[3] != "" {
			second = atoi(m[3])
		}
	} else if m := clockPattern.FindStringSubmatch(s); m != nil && atoi(m[1]) >= 1 && atoi(m[1]) <= 12 {
		hour, minute = atoi(m[1])%12, atoi(m[2])
		if m[3] != "" {
			second = atoi(m[3])
		}
		if m[4] == "p" || m[4] == "P" {
			hour += 12
		}
	} else {
		return "", fmt.Errorf("%q is not a time of day", s)
	}
	if hour > 23 || minute > 59 || second > 59 {
		return "", fmt.Errorf("%q is not a time of day", s)
	}
	out := fmt.Sprintf("%02d:%02d:%02d", hour, minute, second)
	if fraction = strings.TrimRight(fraction, "0"); fraction != "" {
		out += "." + fraction
	}
	return `"` + out + `"`, nil
}

// durationRule writes a duration in the scalar core's canonical form: under
// a second, the largest of ms, us and ns that holds it whole (500ms, 1500us);
// from a second, hours and minutes when present, then seconds with their
// fraction (1h30m0s, 1m30s, 1.5s); 0s for zero. It reads the interval text
// Postgres renders with IntervalStyle postgres, the default (01:30:00,
// 1 day 02:00:00, -1 days +02:00:00), where a day is 24 hours, and a
// duration string such as Go's (1h30m0s, 1.5ms). Months and years, which
// have no fixed length, are refused.
func durationRule(value any) (string, error) {
	s, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string", describe(value))
	}
	nanos, err := intervalNanos(s)
	if err != nil {
		d, goErr := time.ParseDuration(s)
		if goErr != nil {
			return "", err
		}
		nanos = int64(d)
	}
	return `"` + formatDuration(nanos) + `"`, nil
}

// intervalNanos reads Postgres interval text in its postgres style.
func intervalNanos(s string) (int64, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0, fmt.Errorf("%q is not a duration", s)
	}
	total := new(big.Int)
	for i := 0; i < len(fields); i++ {
		field := fields[i]
		if m := intervalTime.FindStringSubmatch(field); m != nil {
			part := big.NewInt(int64(atoi(m[3])))
			part.Mul(part, big.NewInt(60)).Add(part, big.NewInt(int64(atoi(m[4]))))
			hours, ok := new(big.Int).SetString(m[2], 10)
			if !ok {
				return 0, fmt.Errorf("%q is not a duration", s)
			}
			part.Add(part, hours.Mul(hours, big.NewInt(3600)))
			part.Mul(part, big.NewInt(int64(time.Second)))
			part.Add(part, big.NewInt(int64(atoi((m[5] + "000000000")[:9]))))
			if m[1] == "-" {
				part.Neg(part)
			}
			total.Add(total, part)
			continue
		}
		if !intervalCount.MatchString(field) || i+1 == len(fields) {
			return 0, fmt.Errorf("%q is not a Postgres interval", s)
		}
		count, _ := new(big.Int).SetString(strings.TrimPrefix(field, "+"), 10)
		i++
		switch fields[i] {
		case "day", "days":
			total.Add(total, count.Mul(count, big.NewInt(int64(24*time.Hour))))
		case "mon", "mons", "year", "years":
			return 0, fmt.Errorf("%q has months or years, which have no fixed length", s)
		default:
			return 0, fmt.Errorf("%q is not a Postgres interval", s)
		}
	}
	if !total.IsInt64() {
		return 0, fmt.Errorf("%q is too long a duration", s)
	}
	return total.Int64(), nil
}

// formatDuration is the scalar core's canonical duration text.
func formatDuration(nanos int64) string {
	if nanos == 0 {
		return "0s"
	}
	sign := ""
	remaining := uint64(nanos)
	if nanos < 0 {
		sign = "-"
		remaining = uint64(-(nanos + 1)) + 1
	}
	const (
		microsecond = uint64(time.Microsecond)
		millisecond = uint64(time.Millisecond)
		second      = uint64(time.Second)
		minute      = uint64(time.Minute)
		hour        = uint64(time.Hour)
	)
	if remaining < second {
		switch {
		case remaining%millisecond == 0:
			return sign + strconv.FormatUint(remaining/millisecond, 10) + "ms"
		case remaining%microsecond == 0:
			return sign + strconv.FormatUint(remaining/microsecond, 10) + "us"
		}
		return sign + strconv.FormatUint(remaining, 10) + "ns"
	}
	hours := remaining / hour
	remaining %= hour
	minutes := remaining / minute
	remaining %= minute
	seconds := strconv.FormatUint(remaining/second, 10)
	if fraction := remaining % second; fraction != 0 {
		seconds += "." + strings.TrimRight(fmt.Sprintf("%09d", fraction), "0")
	}
	out := sign
	if hours > 0 {
		out += strconv.FormatUint(hours, 10) + "h"
	}
	if hours > 0 || minutes > 0 {
		out += strconv.FormatUint(minutes, 10) + "m"
	}
	return out + seconds + "s"
}

// atoi reads digits a pattern has already matched.
func atoi(digits string) int {
	n, _ := strconv.Atoi(digits)
	return n
}
