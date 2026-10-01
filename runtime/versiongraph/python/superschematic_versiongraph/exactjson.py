"""Exact JSON: a reader and a writer that keep every number as the text it
was written with, so an integer or a numeric wider than a double survives a
round trip. The engine and the canonical rules carry rows as JSON text and
read them with these.

``loads`` reads one JSON value: an object is a ``dict`` (a repeated member
keeps its last value, as Go's decoder does), an array a ``list``, a number a
``JsonNumber``. A lone surrogate escape reads as U+FFFD, as Go reads it.
``dumps`` writes a value compactly, members in their order, strings as the
core's canonical JSON escapes them.
"""

import json
import re
from typing import Any, Dict, List, NoReturn, Union

__all__ = [
    "JsonNumber",
    "JsonValue",
    "loads",
    "dumps",
    "write_string",
    "json_equal",
]


class JsonNumber:
    """A JSON number, kept as the text it was written with."""

    __slots__ = ("text",)

    def __init__(self, text: str) -> None:
        self.text = text

    def __repr__(self) -> str:
        return f"JsonNumber({self.text!r})"

    def __eq__(self, other: object) -> bool:
        return isinstance(other, JsonNumber) and other.text == self.text

    def __hash__(self) -> int:
        return hash(self.text)


JsonValue = Union[None, bool, str, JsonNumber, List[Any], Dict[str, Any]]
"""A JSON value with its numbers kept exact."""


def _refuse_constant(name: str) -> NoReturn:
    raise ValueError(f"{name} is not JSON")


_decoder = json.JSONDecoder(
    parse_float=JsonNumber,
    parse_int=JsonNumber,
    parse_constant=_refuse_constant,
)

# A surrogate escape, which may be lone: json reads a lone one into the
# string as it is, where Go's decoder reads U+FFFD.
_surrogate_escape = re.compile(r"\\u[dD][89a-fA-F]")
_surrogate = re.compile("[\ud800-\udfff]")


def loads(text: str) -> Any:
    """Reads one JSON value, with only whitespace around it, keeping every
    number as a JsonNumber. Raises ValueError for anything else."""
    value = _decoder.decode(text)
    if _surrogate_escape.search(text) is not None:
        value = _replace_surrogates(value)
    return value


def _replace_surrogates(value: Any) -> Any:
    if isinstance(value, str):
        return _surrogate.sub("�", value)
    if isinstance(value, list):
        return [_replace_surrogates(v) for v in value]
    if isinstance(value, dict):
        return {_replace_surrogates(k): _replace_surrogates(v) for k, v in value.items()}
    return value


def dumps(value: Any) -> str:
    """Writes a JSON value compactly: members in their order, a JsonNumber as
    its text, strings as write_string writes them. A Python int is written as
    its digits; a float is refused, since its text is not exact JSON."""
    out: List[str] = []
    _write(out, value)
    return "".join(out)


def _write(out: List[str], value: Any) -> None:
    if value is None:
        out.append("null")
    elif value is True:
        out.append("true")
    elif value is False:
        out.append("false")
    elif isinstance(value, str):
        out.append(write_string(value))
    elif isinstance(value, JsonNumber):
        out.append(value.text)
    elif isinstance(value, int):
        out.append(str(value))
    elif isinstance(value, (list, tuple)):
        out.append("[")
        for i, element in enumerate(value):
            if i > 0:
                out.append(",")
            _write(out, element)
        out.append("]")
    elif isinstance(value, dict):
        out.append("{")
        first = True
        for name, member in value.items():
            if not isinstance(name, str):
                raise TypeError(f"a JSON object's member name is a string, not {name!r}")
            if not first:
                out.append(",")
            first = False
            out.append(write_string(name))
            out.append(":")
            _write(out, member)
        out.append("}")
    else:
        raise TypeError(f"{value!r} is not an exact JSON value")


_escapes = {
    '"': '\\"',
    "\\": "\\\\",
    "\b": "\\b",
    "\f": "\\f",
    "\n": "\\n",
    "\r": "\\r",
    "\t": "\\t",
}
_needs_escape = re.compile('["\\\\\x00-\x1f\ud800-\udfff]')


def _escape(match: "re.Match[str]") -> str:
    c = match.group()
    named = _escapes.get(c)
    if named is not None:
        return named
    if c >= "\ud800":
        # A lone surrogate, which UTF-8 cannot carry, as Go writes it.
        return "�"
    return "\\u%04x" % ord(c)


def write_string(s: str) -> str:
    """Writes a JSON string as the core's canonical JSON does: ``"`` and ``\\``
    escaped, \\b, \\f, \\n, \\r and \\t by name, every other control character
    as \\u00xx in lowercase hex, and everything else as it is."""
    return '"' + _needs_escape.sub(_escape, s) + '"'


def json_equal(a: Any, b: Any) -> bool:
    """Whether two JSON values are equal: numbers by their text, objects by
    their members whatever their order."""
    if isinstance(a, bool) or isinstance(b, bool):
        return a is b
    if isinstance(a, JsonNumber) or isinstance(b, JsonNumber):
        return isinstance(a, JsonNumber) and isinstance(b, JsonNumber) and a.text == b.text
    if isinstance(a, list):
        return isinstance(b, list) and len(a) == len(b) and all(json_equal(x, y) for x, y in zip(a, b))
    if isinstance(a, dict):
        if not isinstance(b, dict) or a.keys() != b.keys():
            return False
        return all(json_equal(a[k], b[k]) for k in a)
    return type(a) is type(b) and a == b
