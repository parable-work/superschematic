"""A schema pattern rewritten so Python's re matches what RE2 matches.

A schema pattern is RE2 syntax, as Go's regexp reads it. This is a port of
internal/generator/patterndialect (its Python target), whose comment lists
what changes. In short: \\d, \\w, \\s and \\b are ASCII (\\s is [\\t\\n\\f\\r ],
without \\v), $ and \\z are the text's end (written \\Z: re's $ also matches
before a final newline), (?m), (?s) and (?U) are applied by rewriting,
(?i) becomes a scoped (?i:...) group (the global flag when the pattern
opens with it), \\pL and the other general categories become code point
ranges from unicodedata (re has no \\p; a script name is refused), and
[[:alpha:]], \\Q...\\E, \\x{...} and octal escapes are spelled out. The
result is compiled with no flags. runtime/schema/testdata/pattern_dialect.json
holds the cases every runtime checks.
"""

from __future__ import annotations

import re
import sys
import unicodedata
from functools import lru_cache
from typing import Dict, List, Optional, Pattern, Tuple

Range = Tuple[int, int]

_POSIX: Dict[str, List[Range]] = {
    "alnum": [(0x30, 0x39), (0x41, 0x5A), (0x61, 0x7A)],
    "alpha": [(0x41, 0x5A), (0x61, 0x7A)],
    "ascii": [(0, 0x7F)],
    "blank": [(0x09, 0x09), (0x20, 0x20)],
    "cntrl": [(0, 0x1F), (0x7F, 0x7F)],
    "digit": [(0x30, 0x39)],
    "graph": [(0x21, 0x7E)],
    "lower": [(0x61, 0x7A)],
    "print": [(0x20, 0x7E)],
    "punct": [(0x21, 0x2F), (0x3A, 0x40), (0x5B, 0x60), (0x7B, 0x7E)],
    "space": [(0x09, 0x0D), (0x20, 0x20)],
    "upper": [(0x41, 0x5A)],
    "word": [(0x30, 0x39), (0x41, 0x5A), (0x5F, 0x5F), (0x61, 0x7A)],
    "xdigit": [(0x30, 0x39), (0x41, 0x46), (0x61, 0x66)],
}

_SPACE: List[Range] = [(0x09, 0x0A), (0x0C, 0x0D), (0x20, 0x20)]
_WORD = "[0-9A-Za-z_]"
_SHORT_ESCAPES = {0x09: "\\t", 0x0A: "\\n", 0x0B: "\\v", 0x0C: "\\f", 0x0D: "\\r"}


@lru_cache(maxsize=None)
def compile_pattern(pattern: str) -> Pattern[str]:
    """The pattern compiled to match as RE2 does; raises re.error when it
    does not compile."""
    return re.compile(translate_pattern(pattern))


@lru_cache(maxsize=None)
def translate_pattern(pattern: str) -> str:
    """The pattern, rewritten for re.compile with no flags."""
    return _Translator(pattern).run()


def _perl_class(c: str) -> List[Range]:
    lower = c.lower()
    if lower == "d":
        return _POSIX["digit"]
    if lower == "w":
        return _POSIX["word"]
    return _SPACE


def _complement(ranges: List[Range]) -> List[Range]:
    out: List[Range] = []
    nxt = 0
    for lo, hi in ranges:
        if lo > nxt:
            out.append((nxt, lo - 1))
        nxt = hi + 1
    if nxt <= sys.maxunicode:
        out.append((nxt, sys.maxunicode))
    return out


@lru_cache(maxsize=None)
def _category_ranges(name: str) -> Optional[Tuple[Range, ...]]:
    """The code points of a general category (L, Lu, ...), or None for a name
    that is none. LC is Lu, Ll and Lt, as in RE2."""
    if name == "Any":
        return ((0, sys.maxunicode),)
    if name == "LC":
        members = {"Lu", "Ll", "Lt"}
    elif len(name) == 1 and name in "CLMNPSZ":
        members = None
    elif len(name) == 2 and name[0] in "CLMNPSZ" and name[1].islower():
        members = {name}
    else:
        return None
    out: List[Range] = []
    for cp in range(sys.maxunicode + 1):
        category = unicodedata.category(chr(cp))
        if (category[0] == name) if members is None else (category in members):
            if out and out[-1][1] == cp - 1:
                out[-1] = (out[-1][0], cp)
            else:
                out.append((cp, cp))
    if not out:
        return None
    return tuple(out)


def _repetition(rest: List[str]) -> int:
    """The length of the {n}, {n,} or {n,m} rest (after its {) opens,
    through its }, or 0."""
    i = 0

    def digits() -> int:
        nonlocal i
        start = i
        while i < len(rest) and "0" <= rest[i] <= "9":
            i += 1
        return i - start

    if digits() == 0:
        return 0
    if i < len(rest) and rest[i] == ",":
        i += 1
        digits()
    if i < len(rest) and rest[i] == "}":
        return i + 1
    return 0


def _is_hex(c: Optional[str]) -> bool:
    return c is not None and len(c) == 1 and c in "0123456789abcdefABCDEF"


def _is_octal(c: Optional[str]) -> bool:
    return c is not None and len(c) == 1 and "0" <= c <= "7"


def _is_alnum(c: str) -> bool:
    return c.isascii() and c.isalnum()


def _is_punctuation(c: str) -> bool:
    return ord(c) < 0x80 and not _is_alnum(c)


def _code_point(cp: int) -> str:
    if cp in _SHORT_ESCAPES:
        return _SHORT_ESCAPES[cp]
    if cp < 0x100:
        return "\\x%02x" % cp
    if cp < 0x10000:
        return "\\u%04x" % cp
    return "\\U%08x" % cp


class _Frame:
    __slots__ = ("flags", "base_i", "mods")

    def __init__(self, flags: Dict[str, bool], base_i: bool) -> None:
        self.flags = flags
        self.base_i = base_i
        self.mods = 0


class _Translator:
    def __init__(self, pattern: str) -> None:
        self.src = list(pattern)
        self.pos = 0
        self.out: List[str] = []
        self.stack = [_Frame({"i": False, "m": False, "s": False, "U": False}, False)]

    def _at(self, i: int) -> Optional[str]:
        return self.src[i] if i < len(self.src) else None

    def run(self) -> str:
        # A pattern that opens with (?i) is caseless throughout: re's global
        # flag, which it reads only first.
        if self.src[:2] == ["(", "?"]:
            leading = self._parse_flags(2, self.stack[0].flags)
            if leading is not None and leading[2] == ")" and leading[0]["i"]:
                self.stack[0].flags["i"] = True
                self.stack[0].base_i = True
                self.out.append("(?i)")
        while self.pos < len(self.src):
            c = self.src[self.pos]
            self.pos += 1
            top = self.stack[-1]
            if c == "\\":
                self._escape()
            elif c == "[":
                self._class()
            elif c == "(":
                self._open()
            elif c == ")":
                self._close()
            elif c == "|":
                self._close_mods(top)
                self.out.append("|")
                if top.flags["i"] != top.base_i:
                    self._open_mod(top)
            elif c == ".":
                self.out.append("[\\s\\S]" if top.flags["s"] else ".")
            elif c == "^":
                self.out.append("(?<![^\\n])" if top.flags["m"] else "^")
            elif c == "$":
                self.out.append("(?![^\\n])" if top.flags["m"] else "\\Z")
            elif c in "*+?":
                self.out.append(c)
                self._quantifier_suffix(top)
            elif c == "{":
                n = _repetition(self.src[self.pos :])
                if n > 0:
                    self.out.append("{" + "".join(self.src[self.pos : self.pos + n]))
                    self.pos += n
                    self._quantifier_suffix(top)
                else:
                    self.out.append("\\{")
            else:
                self._literal(c)
        self._close_mods(self.stack[0])
        return "".join(self.out)

    def _quantifier_suffix(self, top: _Frame) -> None:
        lazy = self._at(self.pos) == "?"
        if lazy:
            self.pos += 1
        if top.flags["U"]:
            lazy = not lazy
        if lazy:
            self.out.append("?")

    def _parse_flags(
        self, at: int, base: Dict[str, bool]
    ) -> Optional[Tuple[Dict[str, bool], int, str]]:
        flags = dict(base)
        on = True
        letters = 0
        for i in range(at, len(self.src)):
            c = self.src[i]
            if c in "imsU":
                flags[c] = on
                letters += 1
            elif c == "-":
                if not on:
                    return None
                on = False
                letters = 0
            elif c in "):":
                if (c == ")" and letters == 0 and on) or (not on and letters == 0):
                    return None
                return flags, i + 1, c
            else:
                return None
        return None

    def _open(self) -> None:
        parent = self.stack[-1]
        rest = "".join(self.src[self.pos :])
        if rest.startswith("?P<") or (
            rest.startswith("?<") and not rest.startswith("?<=") and not rest.startswith("?<!")
        ):
            start = rest.index("<") + 1
            end = rest.find(">")
            if end > start:
                self.out.append("(?P<" + rest[start:end] + ">")
                self.pos += end + 1
                self.stack.append(_Frame(dict(parent.flags), parent.flags["i"]))
                return
        if rest.startswith("?"):
            group = self._parse_flags(self.pos + 1, parent.flags)
            if group is not None:
                flags, end, closer = group
                self.pos = end
                if closer == ")":
                    was = parent.flags["i"]
                    parent.flags = flags
                    if flags["i"] != was:
                        self._open_mod(parent)
                    return
                if flags["i"] == parent.flags["i"]:
                    self.out.append("(?:")
                else:
                    self.out.append("(?i:" if flags["i"] else "(?-i:")
                self.stack.append(_Frame(flags, flags["i"]))
                return
        self.out.append("(")
        self.stack.append(_Frame(dict(parent.flags), parent.flags["i"]))

    def _open_mod(self, top: _Frame) -> None:
        self.out.append("(?i:" if top.flags["i"] else "(?-i:")
        top.mods += 1

    def _close_mods(self, top: _Frame) -> None:
        self.out.append(")" * top.mods)
        top.mods = 0

    def _close(self) -> None:
        if len(self.stack) > 1:
            self._close_mods(self.stack[-1])
            self.stack.pop()
        self.out.append(")")

    def _escape(self) -> None:
        if self.pos >= len(self.src):
            self.out.append("\\")
            return
        c = self.src[self.pos]
        self.pos += 1
        if c in "dwDWsS":
            self._set(_perl_class(c), c in "DWS")
        elif c in "bB":
            if c == "b":
                self.out.append(
                    "(?:(?<=%s)(?!%s)|(?<!%s)(?=%s))" % (_WORD, _WORD, _WORD, _WORD)
                )
            else:
                self.out.append(
                    "(?:(?<=%s)(?=%s)|(?<!%s)(?!%s))" % (_WORD, _WORD, _WORD, _WORD)
                )
        elif c == "A":
            self.out.append("\\A")
        elif c == "z":
            self.out.append("\\Z")
        elif c in "pP":
            self._property(c == "P", False)
        elif c == "Q":
            while self.pos < len(self.src):
                if self.src[self.pos] == "\\" and self._at(self.pos + 1) == "E":
                    self.pos += 2
                    return
                self._literal(self.src[self.pos])
                self.pos += 1
        elif _is_punctuation(c):
            self.out.append("\\" + c)
        else:
            cp = self._escaped_char(c)
            if cp is None:
                self.out.append("\\" + c)
            else:
                self._literal(chr(cp))

    def _escaped_char(self, c: str) -> Optional[int]:
        """The one character the escape \\c (c read) stands for, or None."""
        if c == "x":
            if self._at(self.pos) == "{":
                end = self.pos + 1
                while _is_hex(self._at(end)):
                    end += 1
                if end == self.pos + 1 or self._at(end) != "}":
                    return None
                cp = int("".join(self.src[self.pos + 1 : end]), 16)
                self.pos = end + 1
                return cp if cp <= sys.maxunicode else None
            if _is_hex(self._at(self.pos)) and _is_hex(self._at(self.pos + 1)):
                cp = int(self.src[self.pos] + self.src[self.pos + 1], 16)
                self.pos += 2
                return cp
            return None
        if _is_octal(c):
            # A lone \1 to \7 is a backreference, which RE2 refuses.
            if c != "0" and not _is_octal(self._at(self.pos)):
                return None
            cp = int(c)
            for _ in range(2):
                if not _is_octal(self._at(self.pos)):
                    break
                cp = cp * 8 + int(self.src[self.pos])
                self.pos += 1
            return cp
        controls = {"a": 7, "f": 12, "t": 9, "n": 10, "r": 13, "v": 11}
        if c in controls:
            return controls[c]
        if ord(c) < 0x80 and not _is_alnum(c):
            return ord(c)
        return None

    def _literal(self, c: str) -> None:
        if c in "\\.+*?()|[]{}^$":
            self.out.append("\\" + c)
        else:
            self.out.append(self._char(ord(c)))

    @staticmethod
    def _char(cp: int) -> str:
        if cp < 0x20 or cp > 0x7E:
            return _code_point(cp)
        return chr(cp)

    def _class_char(self, cp: int) -> str:
        # re warns that [[, &&, ~~ and || may become set operations.
        c = chr(cp)
        if c in "\\]-[^&~|":
            return "\\" + c
        return self._char(cp)

    def _set(self, ranges: List[Range], negated: bool) -> None:
        self.out.append(("[^" if negated else "[") + self._ranges(ranges) + "]")

    def _ranges(self, ranges) -> str:
        parts = []
        for lo, hi in ranges:
            parts.append(self._class_char(lo))
            if hi > lo:
                if hi > lo + 1:
                    parts.append("-")
                parts.append(self._class_char(hi))
        return "".join(parts)

    def _class(self) -> None:
        self.out.append("[")
        if self._at(self.pos) == "^":
            self.out.append("^")
            self.pos += 1
        first = True
        while self.pos < len(self.src):
            if self.src[self.pos] == "]" and not first:
                self.pos += 1
                self.out.append("]")
                return
            # A - first or last is a literal as it stands.
            if self.src[self.pos] == "-" and (first or self._at(self.pos + 1) == "]"):
                self.pos += 1
                self.out.append("-")
                first = False
                continue
            first = False
            if self._class_set():
                continue
            self.out.append(self._class_member())
            if (
                self._at(self.pos) == "-"
                and self.pos + 1 < len(self.src)
                and self.src[self.pos + 1] != "]"
            ):
                # A range. RE2 refuses one that ends in a class, as re
                # refuses what this writes for it.
                self.pos += 1
                self.out.append("-" + self._class_member())

    def _class_member(self) -> str:
        c = self.src[self.pos]
        self.pos += 1
        if c != "\\":
            return self._class_char(ord(c))
        if self.pos >= len(self.src):
            return "\\"
        e = self.src[self.pos]
        self.pos += 1
        if _is_punctuation(e):
            # re reads an escaped punctuation mark as the mark, as RE2 does.
            return "\\" + e
        cp = self._escaped_char(e)
        return "\\" + e if cp is None else self._class_char(cp)

    def _class_set(self) -> bool:
        c0 = self._at(self.pos)
        c1 = self._at(self.pos + 1)
        if c0 == "[" and c1 == ":":
            rest = "".join(self.src[self.pos + 2 :])
            end = rest.find(":]")
            if end < 0:
                return False
            name = rest[:end]
            negated = name.startswith("^")
            key = name[1:] if negated else name
            if key not in _POSIX:
                return False
            self.pos += 2 + len(name) + 2
            ranges = _POSIX[key]
            self.out.append(self._ranges(_complement(ranges) if negated else ranges))
            return True
        if c0 != "\\" or c1 is None:
            return False
        if c1 in "dwDWsS":
            self.pos += 2
            ranges = _perl_class(c1)
            self.out.append(self._ranges(_complement(ranges) if c1 in "DWS" else ranges))
            return True
        if c1 in "pP":
            self.pos += 2
            self._property(c1 == "P", True)
            return True
        return False

    def _property(self, negated: bool, in_class: bool) -> None:
        """\\pX, \\p{Name} or \\p{^Name}, its \\p or \\P read."""
        if self.pos >= len(self.src):
            self.out.append("\\p")
            return
        if self.src[self.pos] == "{":
            rest = "".join(self.src[self.pos :])
            end = rest.find("}")
            if end < 0:
                self.out.append("\\p")
                return
            name = rest[1:end]
            self.pos += end + 1
        else:
            name = self.src[self.pos]
            self.pos += 1
        if name.startswith("^"):
            negated = not negated
            name = name[1:]
        ranges = _category_ranges(name)
        if ranges is None:
            # re has no \p: the pattern is refused, as RE2 refuses a class
            # it does not know.
            self.out.append("\\p{" + name + "}")
            return
        if in_class:
            self.out.append(self._ranges(_complement(list(ranges)) if negated else ranges))
        else:
            self._set(list(ranges), negated)
