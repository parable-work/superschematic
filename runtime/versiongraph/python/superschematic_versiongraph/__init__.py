"""superschematic_versiongraph: the version-graph core for Python.

The core (runtime/versiongraph/rust) composes, merges, diffs, hashes and
validates trees of versioned rows. This package calls it natively through a
PyO3 extension module (``superschematic_versiongraph._native``), with typed
``compose``, ``merge``, ``diff``, ``content_hash`` and ``validate`` over its
JSON contract (runtime/versiongraph/README.md) and ``run`` for JSON text.

Each operation encodes its input as JSON, runs the core, and decodes the
output document. A refused input raises ``VersionGraphError``, whose ``code``
is the contract's error code.
"""

import json
from typing import Any, Callable, Optional, Union, cast

from . import _native
from .contract import *  # noqa: F401,F403
from .contract import (
    ComposeInput,
    ComposeOutput,
    ContentHashOutput,
    DiffInput,
    DiffOutput,
    ErrorCode,
    MergeInput,
    MergeOutput,
    OperationName,
    TreeInput,
    ValidateOutput,
)
from .contract import __all__ as _contract_all

__all__ = [
    "VersionGraph",
    "VersionGraphError",
    "compose",
    "merge",
    "diff",
    "content_hash",
    "validate",
    "run",
] + _contract_all


class VersionGraphError(Exception):
    """An input the core refused. ``code`` is stable; ``message`` names the
    offending part of the input."""

    code: ErrorCode
    message: str

    def __init__(self, code: ErrorCode, message: str) -> None:
        super().__init__(f"{code}: {message}")
        self.code = code
        self.message = message


def _dumps(value: Any) -> str:
    # Compact, and refusing NaN and the infinities, which JSON has no form for.
    return json.dumps(value, separators=(",", ":"), ensure_ascii=False, allow_nan=False)


class VersionGraph:
    """The core, with the JSON codec its typed operations use.

    ``loads`` decodes every output document and ``dumps`` encodes every
    input. The defaults are ``json.loads`` and a compact ``json.dumps``. They
    keep an integer's digits however wide, but read a number with a fraction
    or an exponent as a float, so a numeric column that a double does not
    hold exactly loses digits on the way through. Pass a codec that keeps
    them (``json.loads`` with ``parse_float=decimal.Decimal`` and an encoder
    that writes a Decimal's digits, say) when rows carry such values, or use
    ``run`` with JSON text.
    """

    def __init__(
        self,
        *,
        loads: Optional[Callable[[str], Any]] = None,
        dumps: Optional[Callable[[Any], str]] = None,
    ) -> None:
        self._loads = loads if loads is not None else json.loads
        self._dumps = dumps if dumps is not None else _dumps

    def compose(self, input: ComposeInput) -> ComposeOutput:
        """Lays one ref's rows over a base tree by entity key."""
        return cast(ComposeOutput, self._typed("compose", input))

    def merge(self, input: MergeInput) -> MergeOutput:
        """A three-way merge per entity, then per conflict unit."""
        return cast(MergeOutput, self._typed("merge", input))

    def diff(self, input: DiffInput) -> DiffOutput:
        """Each entity's ADD, UPDATE or DELETE from one tree to another."""
        return cast(DiffOutput, self._typed("diff", input))

    def content_hash(self, input: TreeInput) -> ContentHashOutput:
        """SHA-256 over the canonical JSON of each kind's content columns."""
        return cast(ContentHashOutput, self._typed("content_hash", input))

    def validate(self, input: TreeInput) -> ValidateOutput:
        """Duplicate keys, the singleton rule, absent parents, cycles and orders out of range."""
        return cast(ValidateOutput, self._typed("validate", input))

    def run(self, operation: OperationName, input: Union[str, bytes]) -> str:
        """Runs one operation on a JSON document and returns the output
        document as text, untouched by ``loads``. Raises VersionGraphError for
        a refused input, as the typed operations do, and ValueError for an
        operation the core does not have."""
        data = input.encode("utf-8") if isinstance(input, str) else input
        ok, output = _native.run(operation, data)
        if ok:
            return output
        raise _error_from(output)

    def _typed(self, operation: OperationName, input: Any) -> Any:
        return self._loads(self.run(operation, self._dumps(input)))


def _error_from(output: str) -> Exception:
    try:
        document = json.loads(output)
    except ValueError:
        return RuntimeError(f"versiongraph: unreadable error document: {output}")
    error = document.get("error") if isinstance(document, dict) else None
    if (
        not isinstance(error, dict)
        or not isinstance(error.get("code"), str)
        or not isinstance(error.get("message"), str)
    ):
        return RuntimeError(f"versiongraph: unreadable error document: {output}")
    return VersionGraphError(error["code"], error["message"])


_default = VersionGraph()

compose = _default.compose
merge = _default.merge
diff = _default.diff
content_hash = _default.content_hash
validate = _default.validate
run = _default.run
