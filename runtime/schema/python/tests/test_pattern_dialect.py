"""Schema patterns match as RE2 matches them.

``runtime/schema/testdata/pattern_dialect.json`` holds patterns with texts
and whether Go's regexp finds a match; ``internal/generator/patterndialect``
writes it, and the TypeScript and Rust runtime suites assert the same rows.
"""

from __future__ import annotations

import json
import re
from pathlib import Path

import pytest

from superschematic_schema_runtime.validation.pattern import compile_pattern

_CORPUS = json.loads(
    (Path(__file__).resolve().parents[2] / "testdata" / "pattern_dialect.json").read_text()
)


@pytest.mark.parametrize(
    "case", _CORPUS["cases"], ids=lambda c: "%s against %r" % (c["pattern"], c["text"])
)
def test_pattern_matches_as_re2(case):
    pattern = case["pattern"]
    if "\\p{Greek}" in pattern:
        # re has no scripts: the pattern is refused, so its rule refuses
        # every value.
        with pytest.raises(re.error):
            compile_pattern(pattern)
        return
    assert (compile_pattern(pattern).search(case["text"]) is not None) == case["match"]
