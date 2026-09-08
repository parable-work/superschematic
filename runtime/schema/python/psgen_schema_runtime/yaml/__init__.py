"""YAML payload loader.

Wraps ``yaml.safe_load`` so the facade can accept YAML bytes/string for
``load_type_yaml`` / ``load_input_yaml``. PyYAML is an optional dependency
installed via the ``yaml`` extra (``pip install parable-scalar-lib[yaml]``).
"""

from __future__ import annotations

from typing import Any

from .. import ValidationError
from ..errors import ValidationErrors, append_field_error, new_validation_errors


def load_yaml_object(payload: str | bytes) -> tuple[Any, ValidationErrors]:
    """Decode ``payload`` as YAML. Returns ``(value, errors)``.

    Raises :class:`ModuleNotFoundError` if PyYAML is not installed.
    """
    errors = new_validation_errors()
    try:
        import yaml  # type: ignore[import-not-found]
    except ModuleNotFoundError as exc:
        append_field_error(
            errors,
            "",
            ValidationError(
                validator="yaml",
                message="PyYAML is not installed; install parable-scalar-lib[yaml]",
            ),
        )
        raise exc

    if isinstance(payload, bytes):
        try:
            text = payload.decode("utf-8")
        except UnicodeDecodeError as exc:
            append_field_error(
                errors, "", ValidationError(validator="yaml", message=str(exc))
            )
            return None, errors
    else:
        text = payload
    try:
        return yaml.safe_load(text), errors
    except yaml.YAMLError as exc:  # type: ignore[attr-defined]
        append_field_error(errors, "", ValidationError(validator="yaml", message=str(exc)))
        return None, errors


__all__ = ["load_yaml_object"]
