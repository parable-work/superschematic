"""The runtime schema dataclasses keep in step with their TypeScript twin.

``validation/types.py`` and ``ir/typescript/index.d.ts`` (the
@superschematic/schema-ir package root) are the two hand-written views of the
runtime schema document. Neither is generated: schema-file.json, which
``internal/tools/schemafiletypes`` writes, describes the schema-file data
form, a different document. So this test compares the two by hand: every
interface has a dataclass of its name, every member a field of its snake_case
name, and a member that may be null a field that may be None.
"""

from __future__ import annotations

import dataclasses
import re
from pathlib import Path

from superschematic_schema_runtime.validation import types

_INDEX_D_TS = Path(__file__).resolve().parents[4] / "ir" / "typescript" / "index.d.ts"

# Members the TypeScript view has and the Python one does not. The TypeScript
# runtime carries them for its schema JSON writer and its IR reader, which
# the Python runtime has no counterpart of; nothing here parses, validates or
# serializes by them.
_TS_ONLY = {
    "FieldDef": {"temporalFormat", "uiControl"},
}


def _interfaces() -> dict[str, dict[str, str]]:
    source = _INDEX_D_TS.read_text()
    source = re.sub(r"/\*.*?\*/", "", source, flags=re.S)
    source = re.sub(r"//[^\n]*", "", source)
    out: dict[str, dict[str, str]] = {}
    for name, body in re.findall(r"export interface (\w+) \{(.*?)\n\}", source, re.S):
        out[name] = {key: kind.strip() for key, kind in re.findall(r"(\w+)\??:\s*([^;]+);", body)}
    return out


def _camel(name: str) -> str:
    head, *rest = name.rstrip("_").split("_")
    return head + "".join(part[:1].upper() + part[1:] for part in rest)


def _dataclasses() -> dict[str, type]:
    return {
        name: getattr(types, name)
        for name in types.__all__
        if dataclasses.is_dataclass(getattr(types, name))
    }


def test_every_interface_has_its_dataclass():
    assert sorted(_interfaces()) == sorted(_dataclasses())


def test_every_member_has_its_field():
    interfaces = _interfaces()
    for name, cls in _dataclasses().items():
        fields = {_camel(field.name) for field in dataclasses.fields(cls)}
        members = set(interfaces[name]) - _TS_ONLY.get(name, set())
        assert fields == members, name


def test_a_nullable_member_is_an_optional_field():
    interfaces = _interfaces()
    for name, cls in _dataclasses().items():
        for field in dataclasses.fields(cls):
            member = interfaces[name].get(_camel(field.name))
            if member is not None and "null" in member:
                assert "None" in str(field.type), f"{name}.{field.name}"
