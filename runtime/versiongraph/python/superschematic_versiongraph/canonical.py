"""The canonical row rules (D19): what turns a value as Postgres renders it, in
``to_jsonb`` of a live row or in a history image, or as the schema runtime
writes it, into its canonical JSON. The Python counterpart of the Go
module's package ``canonical``; runtime/versiongraph/README.md ("Canonical
rows") is the contract and runtime/versiongraph/testdata/canonical holds its
vectors.

A canonical row is a JSON object keyed by column name whose values are the
schema runtime's JSON for each field's type, in one canonical form per value
class. A graph descriptor (version 3) gives every column a value class; each
class has one rule. The rules read and write JSON text through ``exactjson``,
so a number keeps its digits.
"""

import re
from typing import Any, Callable, Dict, List, Mapping, Optional, Tuple

from .exactjson import JsonNumber, loads, write_string

__all__ = [
    "ELEMENT_CLASSES",
    "CanonicalError",
    "UnknownClassError",
    "canonical_value",
    "canonical_of",
    "canonical_row",
    "canonical_row_value",
    "uuid_canonical",
    "uuid_hyphenated",
    "duration_nanos",
    "format_duration",
]

ELEMENT_CLASSES: Tuple[str, ...] = (
    "string",
    "integer",
    "number",
    "boolean",
    "uuid",
    "dateTime",
    "date",
    "time",
    "duration",
    "enum",
    "json",
)
"""The element classes. A column's class is one of them, a list of one
(``uuid[]``) or a list of lists (``uuid[][]``)."""


class CanonicalError(ValueError):
    """A value its class's rule refuses, or a row that does not fit its
    columns. ``column`` is the row's column, "" for a single value."""

    def __init__(self, column: str, value_class: str, message: str) -> None:
        if column:
            text = f"canonical: column {column} ({value_class}): {message}"
        else:
            text = f"canonical: {value_class}: {message}"
        super().__init__(text)
        self.column = column
        self.value_class = value_class
        self.detail = message


class UnknownClassError(ValueError):
    """A class that is not an element class, a list of one or a list of
    lists of one."""


class _Refused(Exception):
    """A rule's refusal, before it is named by its class and column."""


_Rule = Callable[[Any], str]


def _describe(value: Any) -> str:
    if isinstance(value, str):
        return write_string(value)
    if isinstance(value, JsonNumber):
        return "the number " + value.text
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, list):
        return "a list"
    if isinstance(value, dict):
        return "an object"
    return repr(value)


def _string_rule(value: Any) -> str:
    if not isinstance(value, str):
        raise _Refused(f"{_describe(value)} is not a string")
    return write_string(value)


def _boolean_rule(value: Any) -> str:
    if not isinstance(value, bool):
        raise _Refused(f"{_describe(value)} is not a boolean")
    return "true" if value else "false"


_integer_pattern = re.compile(r"-?(0|[1-9][0-9]*)")
_number_pattern = re.compile(r"(-?)(0|[1-9][0-9]*)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?")


def _integer_rule(value: Any) -> str:
    if not isinstance(value, JsonNumber) or _integer_pattern.fullmatch(value.text) is None:
        raise _Refused(f"{_describe(value)} is not an integer")
    if value.text == "-0":
        return "0"
    return value.text


def _number_rule(value: Any) -> str:
    """A number's exact decimal value in the layout ECMAScript's
    Number::toString uses: plain digits while the decimal point falls within
    21 digits of the first and no more than 6 zeros follow it, else one
    digit, a fraction and an exponent with its sign. Trailing zeros go, -0
    is 0, and the digits are never rounded."""
    if not isinstance(value, JsonNumber):
        raise _Refused(f"{_describe(value)} is not a number")
    m = _number_pattern.fullmatch(value.text)
    if m is None:
        raise _Refused(f"{write_string(value.text)} is not a JSON number")
    negative, whole, fraction, exponent_text = m.group(1) == "-", m.group(2), m.group(3) or "", m.group(4)
    exponent = 0
    if exponent_text is not None:
        exponent = int(exponent_text)
        if not -(1 << 31) <= exponent < (1 << 31):
            raise _Refused(f"the exponent of {value.text} is out of range")
    # The value is 0.digits * 10^point.
    digits = whole + fraction
    point = len(whole) + exponent
    trimmed = digits.lstrip("0")
    point -= len(digits) - len(trimmed)
    digits = trimmed.rstrip("0")
    if digits == "":
        return "0"
    sign = "-" if negative else ""
    k = len(digits)
    if k <= point <= 21:
        return sign + digits + "0" * (point - k)
    if 0 < point <= 21:
        return sign + digits[:point] + "." + digits[point:]
    if -6 < point <= 0:
        return sign + "0." + "0" * (-point) + digits
    e = point - 1
    exponent_sign = "+"
    if e < 0:
        exponent_sign = "-"
        e = -e
    mantissa = digits[:1]
    if k > 1:
        mantissa += "." + digits[1:]
    return f"{sign}{mantissa}e{exponent_sign}{e}"


def _json_rule(value: Any) -> str:
    """Any JSON value canonically: object members sorted by key, no
    whitespace, strings as the string class writes them and every number as
    the number class does."""
    out: List[str] = []
    _write_json(out, value)
    return "".join(out)


def _write_json(out: List[str], value: Any) -> None:
    if value is None:
        out.append("null")
    elif value is True:
        out.append("true")
    elif value is False:
        out.append("false")
    elif isinstance(value, str):
        out.append(write_string(value))
    elif isinstance(value, JsonNumber):
        out.append(_number_rule(value))
    elif isinstance(value, list):
        out.append("[")
        for i, element in enumerate(value):
            if i > 0:
                out.append(",")
            _write_json(out, element)
        out.append("]")
    elif isinstance(value, dict):
        out.append("{")
        for i, key in enumerate(sorted(value)):
            if i > 0:
                out.append(",")
            out.append(write_string(key))
            out.append(":")
            _write_json(out, value[key])
        out.append("}")
    else:
        raise _Refused(f"{value!r} is not a JSON value")


_BASE62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
_hyphenated_uuid = re.compile(r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}")
_base62_uuid = re.compile(r"[0-9A-Za-z]{1,22}")


def _uuid_int(s: str) -> int:
    if _hyphenated_uuid.fullmatch(s) is not None:
        return int(s.replace("-", ""), 16)
    if _base62_uuid.fullmatch(s) is not None:
        n = 0
        for c in s:
            n = n * 62 + _BASE62.index(c)
        if n.bit_length() > 128:
            raise _Refused(f"{write_string(s)} is wider than a UUID")
        return n
    raise _Refused(f"{write_string(s)} is not a UUID")


def _base62(n: int) -> str:
    if n == 0:
        return "0"
    out: List[str] = []
    while n > 0:
        n, digit = divmod(n, 62)
        out.append(_BASE62[digit])
    return "".join(reversed(out))


def _uuid_rule(value: Any) -> str:
    """A UUID in the scalar core's canonical form, base62 of its 128 bits
    (the nil UUID is "0"), from the hyphenated form in either case or from
    base62."""
    if not isinstance(value, str):
        raise _Refused(f"{_describe(value)} is not a string")
    return '"' + _base62(_uuid_int(value)) + '"'


def uuid_canonical(s: str) -> str:
    """The canonical form (base62) of a UUID given hyphenated or in base62.
    Raises CanonicalError for anything else."""
    try:
        return _base62(_uuid_int(s))
    except _Refused as refused:
        raise CanonicalError("", "uuid", str(refused)) from None


def uuid_hyphenated(s: str) -> str:
    """The hyphenated form Postgres reads of a UUID given in its canonical
    form, or hyphenated."""
    try:
        text = "%032x" % _uuid_int(s)
    except _Refused as refused:
        raise CanonicalError("", "uuid", str(refused)) from None
    return f"{text[0:8]}-{text[8:12]}-{text[12:16]}-{text[16:20]}-{text[20:32]}"


_date_time_pattern = re.compile(
    r"([0-9]{4})-([0-9]{2})-([0-9]{2})[T ]([0-9]{2}):([0-9]{2}):([0-9]{2})(?:\.([0-9]{1,9}))?"
    r"(Z|[+-][0-9]{2}(?::[0-9]{2}(?::[0-9]{2})?)?)"
)
_date_pattern = re.compile(r"([0-9]{4})-([0-9]{2})-([0-9]{2})")
_time_pattern = re.compile(r"([0-9]{2}):([0-9]{2})(?::([0-9]{2})(?:\.([0-9]{1,9}))?)?")
# Go's \s: ASCII whitespace only.
_clock_pattern = re.compile(r"([0-9]{1,2}):([0-9]{2})(?::([0-9]{2}))?[\t\n\f\r ]?([AaPp])[Mm]")
_interval_time = re.compile(r"([+-]?)([0-9]+):([0-9]{2}):([0-9]{2})(?:\.([0-9]{1,9}))?")
_interval_count = re.compile(r"[+-]?[0-9]+")

_SECOND = 1_000_000_000
_DAY_SECONDS = 24 * 3600


def _days_from_civil(year: int, month: int, day: int) -> int:
    """Days since 1970-01-01 of a proleptic Gregorian date."""
    year -= month <= 2
    era = (year if year >= 0 else year - 399) // 400
    yoe = year - era * 400
    doy = (153 * (month + (-3 if month > 2 else 9)) + 2) // 5 + day - 1
    doe = yoe * 365 + yoe // 4 - yoe // 100 + doy
    return era * 146097 + doe - 719468


def _civil_from_days(days: int) -> Tuple[int, int, int]:
    days += 719468
    era = (days if days >= 0 else days - 146096) // 146097
    doe = days - era * 146097
    yoe = (doe - doe // 1460 + doe // 36524 - doe // 146096) // 365
    year = yoe + era * 400
    doy = doe - (365 * yoe + yoe // 4 - yoe // 100)
    mp = (5 * doy + 2) // 153
    day = doy - (153 * mp + 2) // 5 + 1
    month = mp + (3 if mp < 10 else -9)
    return year + (month <= 2), month, day


def _valid_date(year: int, month: int, day: int) -> bool:
    if not 1 <= month <= 12 or day < 1:
        return False
    leap = year % 4 == 0 and (year % 100 != 0 or year % 400 == 0)
    days = (31, 29 if leap else 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31)[month - 1]
    return day <= days


def _offset_seconds(text: str) -> int:
    if text == "Z":
        return 0
    sign = -1 if text[0] == "-" else 1
    parts = text[1:].split(":")
    seconds = int(parts[0]) * 3600
    if len(parts) > 1:
        seconds += int(parts[1]) * 60
    if len(parts) > 2:
        seconds += int(parts[2])
    if seconds >= _DAY_SECONDS:
        raise _Refused(f"offset {text} is a day or more")
    return sign * seconds


def _date_time_rule(value: Any) -> str:
    """An instant as RFC 3339 in UTC with a Z, its fraction of a second
    without trailing zeros and left out when zero: Go's RFC3339Nano of the
    UTC time. It reads the form Postgres renders, whose offset follows the
    session's time zone and may carry seconds, and any RFC 3339 time with an
    offset. A year outside 0000-9999, BC, infinity, a time without an offset
    and an offset of a day or more are refused."""
    if not isinstance(value, str):
        raise _Refused(f"{_describe(value)} is not a string")
    m = _date_time_pattern.fullmatch(value)
    if m is None:
        raise _Refused(f"{write_string(value)} is not a date-time with an offset")
    try:
        offset = _offset_seconds(m.group(8))
    except _Refused as refused:
        raise _Refused(f"{write_string(value)}: {refused}") from None
    year, month, day = int(m.group(1)), int(m.group(2)), int(m.group(3))
    hour, minute, second = int(m.group(4)), int(m.group(5)), int(m.group(6))
    nanos = int(((m.group(7) or "") + "000000000")[:9])
    if not _valid_date(year, month, day) or hour > 23 or minute > 59 or second > 59:
        raise _Refused(f"{write_string(value)} is not a valid date-time")
    seconds = _days_from_civil(year, month, day) * _DAY_SECONDS + hour * 3600 + minute * 60 + second - offset
    days, of_day = divmod(seconds, _DAY_SECONDS)
    year, month, day = _civil_from_days(days)
    if not 0 <= year <= 9999:
        raise _Refused(f"{write_string(value)} falls outside the years 0000-9999")
    out = "%04d-%02d-%02dT%02d:%02d:%02d" % (year, month, day, of_day // 3600, of_day // 60 % 60, of_day % 60)
    if nanos:
        out += ("." + "%09d" % nanos).rstrip("0")
    return '"' + out + 'Z"'


def _date_rule(value: Any) -> str:
    """A calendar date as YYYY-MM-DD, the form Postgres renders. BC,
    infinity and a date that does not exist are refused."""
    if not isinstance(value, str):
        raise _Refused(f"{_describe(value)} is not a string")
    m = _date_pattern.fullmatch(value)
    if m is None:
        raise _Refused(f"{write_string(value)} is not a date")
    if not _valid_date(int(m.group(1)), int(m.group(2)), int(m.group(3))):
        raise _Refused(f"{write_string(value)} is not a valid date")
    return '"' + value + '"'


def _time_rule(value: Any) -> str:
    """A time of day as HH:MM:SS on a 24-hour clock, its fraction of a
    second without trailing zeros and left out when zero, the form Postgres
    renders. It also reads HH:MM and a 12-hour clock (2:30 pm, 12:05:09AM),
    each as Postgres reads it into a time. 24:00:00 is refused, as the
    scalar refuses it."""
    if not isinstance(value, str):
        raise _Refused(f"{_describe(value)} is not a string")
    second = 0
    fraction = ""
    m = _time_pattern.fullmatch(value)
    if m is not None:
        hour, minute, fraction = int(m.group(1)), int(m.group(2)), m.group(4) or ""
        if m.group(3) is not None:
            second = int(m.group(3))
    else:
        c = _clock_pattern.fullmatch(value)
        if c is None or not 1 <= int(c.group(1)) <= 12:
            raise _Refused(f"{write_string(value)} is not a time of day")
        hour, minute = int(c.group(1)) % 12, int(c.group(2))
        if c.group(3) is not None:
            second = int(c.group(3))
        if c.group(4) in ("p", "P"):
            hour += 12
    if hour > 23 or minute > 59 or second > 59:
        raise _Refused(f"{write_string(value)} is not a time of day")
    out = "%02d:%02d:%02d" % (hour, minute, second)
    fraction = fraction.rstrip("0")
    if fraction:
        out += "." + fraction
    return '"' + out + '"'


_INT64_MAX = (1 << 63) - 1


def _go_fields(s: str) -> List[str]:
    """Go's strings.Fields: s split around runs of Unicode white space, as Go's
    unicode.IsSpace reads it (which, unlike str.isspace, leaves out
    U+001C..U+001F)."""
    fields: List[str] = []
    start = -1
    for i, c in enumerate(s):
        space = c.isspace() and c not in "\x1c\x1d\x1e\x1f"
        if space:
            if start >= 0:
                fields.append(s[start:i])
                start = -1
        elif start < 0:
            start = i
    if start >= 0:
        fields.append(s[start:])
    return fields


def _interval_nanos(s: str) -> int:
    """Postgres interval text in its postgres style, where a day is 24
    hours."""
    fields = _go_fields(s)
    if not fields:
        raise _Refused(f"{write_string(s)} is not a duration")
    total = 0
    i = 0
    while i < len(fields):
        field = fields[i]
        m = _interval_time.fullmatch(field)
        if m is not None:
            part = ((int(m.group(2)) * 3600 + int(m.group(3)) * 60 + int(m.group(4))) * _SECOND) + int(
                ((m.group(5) or "") + "000000000")[:9]
            )
            total += -part if m.group(1) == "-" else part
            i += 1
            continue
        if _interval_count.fullmatch(field) is None or i + 1 == len(fields):
            raise _Refused(f"{write_string(s)} is not a Postgres interval")
        count = int(field)
        i += 1
        unit = fields[i]
        if unit in ("day", "days"):
            total += count * _DAY_SECONDS * _SECOND
        elif unit in ("mon", "mons", "year", "years"):
            raise _Refused(f"{write_string(s)} has months or years, which have no fixed length")
        else:
            raise _Refused(f"{write_string(s)} is not a Postgres interval")
        i += 1
    if not -(1 << 63) <= total <= _INT64_MAX:
        raise _Refused(f"{write_string(s)} is too long a duration")
    return total


_UNITS = {
    "ns": 1,
    "us": 1_000,
    "µs": 1_000,
    "μs": 1_000,
    "ms": 1_000_000,
    "s": _SECOND,
    "m": 60 * _SECOND,
    "h": 3600 * _SECOND,
}


def _go_parse_duration(s: str) -> int:
    """Go's time.ParseDuration: [-+]?([0-9]*(\\.[0-9]*)?[a-z]+)+, in
    nanoseconds, with its overflow rules."""
    invalid = _Refused(f"time: invalid duration {write_string(s)}")
    rest = s
    negative = False
    if rest and rest[0] in "+-":
        negative = rest[0] == "-"
        rest = rest[1:]
    if rest == "0":
        return 0
    if rest == "":
        raise invalid
    total = 0
    while rest:
        if not (rest[0] == "." or "0" <= rest[0] <= "9"):
            raise invalid
        # [0-9]*
        i = 0
        whole = 0
        while i < len(rest) and "0" <= rest[i] <= "9":
            if whole > (1 << 63) // 10:
                raise invalid
            whole = whole * 10 + ord(rest[i]) - 48
            if whole > 1 << 63:
                raise invalid
            i += 1
        pre = i > 0
        rest = rest[i:]
        # (\.[0-9]*)?
        fraction, scale, post = 0, 1.0, False
        if rest and rest[0] == ".":
            rest = rest[1:]
            i = 0
            overflow = False
            while i < len(rest) and "0" <= rest[i] <= "9":
                if not overflow:
                    if fraction > _INT64_MAX // 10:
                        overflow = True
                    else:
                        y = fraction * 10 + ord(rest[i]) - 48
                        if y > 1 << 63:
                            overflow = True
                        else:
                            fraction = y
                            scale *= 10
                i += 1
            post = i > 0
            rest = rest[i:]
        if not pre and not post:
            raise invalid
        i = 0
        while i < len(rest) and not (rest[i] == "." or "0" <= rest[i] <= "9"):
            i += 1
        if i == 0:
            raise _Refused(f"time: missing unit in duration {write_string(s)}")
        unit = _UNITS.get(rest[:i])
        if unit is None:
            raise _Refused(f"time: unknown unit {write_string(rest[:i])} in duration {write_string(s)}")
        rest = rest[i:]
        if whole > (1 << 63) // unit:
            raise invalid
        whole *= unit
        if fraction > 0:
            whole += int(float(fraction) * (float(unit) / scale))
            if whole > 1 << 63:
                raise invalid
        total += whole
        if total > 1 << 63:
            raise invalid
    if negative:
        return -total
    if total > _INT64_MAX:
        raise invalid
    return total


def duration_nanos(s: str) -> int:
    """A duration's nanoseconds, from interval text or a duration string.
    Raises CanonicalError for anything else."""
    try:
        return _duration(s)
    except _Refused as refused:
        raise CanonicalError("", "duration", str(refused)) from None


def _duration(s: str) -> int:
    try:
        return _interval_nanos(s)
    except _Refused as interval_error:
        try:
            return _go_parse_duration(s)
        except _Refused:
            raise interval_error from None


def format_duration(nanos: int) -> str:
    """The scalar core's canonical duration text: under a second, the
    largest of ms, us and ns that holds it whole; from a second, hours and
    minutes when present, then seconds with their fraction; 0s for zero."""
    if nanos == 0:
        return "0s"
    sign = "-" if nanos < 0 else ""
    remaining = -nanos if nanos < 0 else nanos
    if remaining < _SECOND:
        if remaining % 1_000_000 == 0:
            return f"{sign}{remaining // 1_000_000}ms"
        if remaining % 1_000 == 0:
            return f"{sign}{remaining // 1_000}us"
        return f"{sign}{remaining}ns"
    hours, remaining = divmod(remaining, 3600 * _SECOND)
    minutes, remaining = divmod(remaining, 60 * _SECOND)
    whole, fraction = divmod(remaining, _SECOND)
    seconds = str(whole)
    if fraction:
        seconds += "." + ("%09d" % fraction).rstrip("0")
    out = sign
    if hours > 0:
        out += f"{hours}h"
    if hours > 0 or minutes > 0:
        out += f"{minutes}m"
    return out + seconds + "s"


def _duration_rule(value: Any) -> str:
    """A duration in the scalar core's canonical form, from Postgres
    interval text (IntervalStyle postgres) or a duration string such as
    Go's. Months and years, which have no fixed length, are refused."""
    if not isinstance(value, str):
        raise _Refused(f"{_describe(value)} is not a string")
    return '"' + format_duration(_duration(value)) + '"'


_RULES: Dict[str, _Rule] = {
    "string": _string_rule,
    "integer": _integer_rule,
    "number": _number_rule,
    "boolean": _boolean_rule,
    "uuid": _uuid_rule,
    "dateTime": _date_time_rule,
    "date": _date_rule,
    "time": _time_rule,
    "duration": _duration_rule,
    "enum": _string_rule,
    "json": _json_rule,
}


def _parse_class(value_class: str) -> Tuple[_Rule, int]:
    element, depth = value_class, 0
    if value_class.endswith("[][]"):
        element, depth = value_class[:-4], 2
    elif value_class.endswith("[]"):
        element, depth = value_class[:-2], 1
    rule = _RULES.get(element)
    if rule is None:
        raise UnknownClassError(f"canonical: unknown value class {write_string(value_class)}")
    return rule, depth


def _apply(rule: _Rule, depth: int, value: Any) -> str:
    """Runs an element rule over a value, a list or a list of lists. A null
    value is null; a null element is refused, since a list element is never
    null (D12)."""
    if value is None:
        return "null"
    if depth == 0:
        return rule(value)
    if not isinstance(value, list):
        raise _Refused(f"{_describe(value)} is not a list")
    out: List[str] = []
    for i, element in enumerate(value):
        if element is None:
            raise _Refused(f"element {i} is null, and a list element is never null")
        try:
            out.append(_apply(rule, depth - 1, element))
        except _Refused as refused:
            raise _Refused(f"element {i}: {refused}") from None
    return "[" + ",".join(out) + "]"


def _decode(text: str, value_class: str, column: str = "") -> Any:
    try:
        return loads(text)
    except ValueError as error:
        raise CanonicalError(column, value_class, str(error)) from None


def canonical_of(value_class: str, value: Any) -> str:
    """The canonical JSON of one value of a class, given as an exact JSON
    value (``exactjson.loads``). JSON null is null in every class."""
    rule, depth = _parse_class(value_class)
    try:
        return _apply(rule, depth, value)
    except _Refused as refused:
        raise CanonicalError("", value_class, str(refused)) from None


def canonical_value(value_class: str, text: str) -> str:
    """The canonical JSON of one value of a class, given as JSON text as
    Postgres renders it inside to_jsonb, or as the schema runtime writes it."""
    rule, depth = _parse_class(value_class)
    value = _decode(text, value_class)
    try:
        return _apply(rule, depth, value)
    except _Refused as refused:
        raise CanonicalError("", value_class, str(refused)) from None


def canonical_row_value(columns: Mapping[str, str], row: Any) -> str:
    """The canonical row of a row given as an exact JSON value, whose columns
    have the classes in columns: a JSON object with its members sorted by
    column name. A column the row has and columns lacks is refused; one
    columns has and the row lacks stays absent."""
    if not isinstance(row, dict):
        raise CanonicalError("", "", "a row is a JSON object")
    out: List[str] = []
    for name in sorted(row):
        value_class = columns.get(name)
        if value_class is None:
            raise CanonicalError(name, "", "the row has a column its descriptor does not declare")
        try:
            rule, depth = _parse_class(value_class)
        except UnknownClassError as error:
            raise UnknownClassError(f"column {name}: {error}") from None
        try:
            member = _apply(rule, depth, row[name])
        except _Refused as refused:
            raise CanonicalError(name, value_class, str(refused)) from None
        out.append(write_string(name) + ":" + member)
    return "{" + ",".join(out) + "}"


def canonical_row(columns: Mapping[str, str], text: str) -> str:
    """The canonical row of a row given as JSON text, as to_jsonb renders it
    or as a typed value serializes (a canonical row is a fixed point)."""
    return canonical_row_value(columns, _decode(text, "", ""))


def _interval_argument(duration: str) -> str:
    """A canonical duration as interval text Postgres reads exactly:
    [-]H:MM:SS with the fraction of a second."""
    nanos = duration_nanos(duration)
    sign = ""
    if nanos < 0:
        sign, nanos = "-", -nanos
    seconds, fraction = divmod(nanos, _SECOND)
    out = "%s%02d:%02d:%02d" % (sign, seconds // 3600, seconds // 60 % 60, seconds % 60)
    if fraction > 0:
        out += ("." + "%09d" % fraction).rstrip("0")
    return out


def postgres_input(value_class: str, value: Any) -> str:
    """A canonical value of a class as the JSON jsonb_populate_record reads
    into its column: a UUID, or a list of them, hyphenated, and a duration,
    or a list of them, as interval text. A json column and a list of lists
    are JSONB and keep the canonical JSON; every other class is already what
    Postgres reads."""
    normalized = canonical_of(value_class, value)
    if value_class.endswith("[][]"):
        return normalized
    depth = 1 if value_class.endswith("[]") else 0
    element = value_class[:-2] if depth else value_class
    convert: Optional[Callable[[str], str]] = None
    if element == "uuid":
        convert = uuid_hyphenated
    elif element == "duration":
        convert = _interval_argument
    if convert is None or normalized == "null":
        return normalized
    parsed = loads(normalized)
    if depth == 0:
        return write_string(convert(parsed))
    return "[" + ",".join(write_string(convert(s)) for s in parsed) + "]"
