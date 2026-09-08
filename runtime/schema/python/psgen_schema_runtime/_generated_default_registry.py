# @generated; do not edit

"""Generated default registries for the psgen schema runtime.

Maps canonical scalar names (e.g. ``Identity.UUID``) to the matching
``parse_<module>``, ``normalize_<module>``, and ``validate_<module>``
function names exported by ``parable_scalars``. Consumed by
``psgen_schema_runtime`` to build the default scalar registries
without runtime reflection.
"""

from __future__ import annotations

from typing import Callable

from parable_scalars import (
    parse_contact_email,
    normalize_contact_email,
    validate_contact_email,
    parse_contact_phone_number,
    normalize_contact_phone_number,
    validate_contact_phone_number,
    parse_design_color,
    normalize_design_color,
    validate_design_color,
    parse_finance_money,
    normalize_finance_money,
    validate_finance_money,
    parse_generic_int64,
    normalize_generic_int64,
    validate_generic_int64,
    parse_generic_json,
    normalize_generic_json,
    validate_generic_json,
    parse_identity_uuid,
    normalize_identity_uuid,
    validate_identity_uuid,
    parse_identity_user_id,
    normalize_identity_user_id,
    validate_identity_user_id,
    parse_parable_permission,
    normalize_parable_permission,
    validate_parable_permission,
    parse_tap_identifier,
    normalize_tap_identifier,
    validate_tap_identifier,
    parse_temporal_date,
    normalize_temporal_date,
    validate_temporal_date,
    parse_temporal_date_time,
    normalize_temporal_date_time,
    validate_temporal_date_time,
    parse_temporal_days,
    normalize_temporal_days,
    validate_temporal_days,
    parse_temporal_duration,
    normalize_temporal_duration,
    validate_temporal_duration,
    parse_temporal_hours,
    normalize_temporal_hours,
    validate_temporal_hours,
    parse_temporal_milliseconds,
    normalize_temporal_milliseconds,
    validate_temporal_milliseconds,
    parse_temporal_minutes,
    normalize_temporal_minutes,
    validate_temporal_minutes,
    parse_temporal_month,
    normalize_temporal_month,
    validate_temporal_month,
    parse_temporal_quarter_year,
    normalize_temporal_quarter_year,
    validate_temporal_quarter_year,
    parse_temporal_seconds,
    normalize_temporal_seconds,
    validate_temporal_seconds,
)


DEFAULT_PARSE_FUNCTIONS: dict[str, Callable[[str], str]] = {
    "Contact.Email": parse_contact_email,
    "Contact.PhoneNumber": parse_contact_phone_number,
    "Design.Color": parse_design_color,
    "Finance.Money": parse_finance_money,
    "Generic.Int64": parse_generic_int64,
    "Generic.JSON": parse_generic_json,
    "Identity.UUID": parse_identity_uuid,
    "Identity.UserID": parse_identity_user_id,
    "Parable.Permission": parse_parable_permission,
    "Tap.Identifier": parse_tap_identifier,
    "Temporal.Date": parse_temporal_date,
    "Temporal.DateTime": parse_temporal_date_time,
    "Temporal.Days": parse_temporal_days,
    "Temporal.Duration": parse_temporal_duration,
    "Temporal.Hours": parse_temporal_hours,
    "Temporal.Milliseconds": parse_temporal_milliseconds,
    "Temporal.Minutes": parse_temporal_minutes,
    "Temporal.Month": parse_temporal_month,
    "Temporal.QuarterYear": parse_temporal_quarter_year,
    "Temporal.Seconds": parse_temporal_seconds,
}


DEFAULT_NORMALIZE_FUNCTIONS: dict[str, Callable[[str], str]] = {
    "Contact.Email": normalize_contact_email,
    "Contact.PhoneNumber": normalize_contact_phone_number,
    "Design.Color": normalize_design_color,
    "Finance.Money": normalize_finance_money,
    "Generic.Int64": normalize_generic_int64,
    "Generic.JSON": normalize_generic_json,
    "Identity.UUID": normalize_identity_uuid,
    "Identity.UserID": normalize_identity_user_id,
    "Parable.Permission": normalize_parable_permission,
    "Tap.Identifier": normalize_tap_identifier,
    "Temporal.Date": normalize_temporal_date,
    "Temporal.DateTime": normalize_temporal_date_time,
    "Temporal.Days": normalize_temporal_days,
    "Temporal.Duration": normalize_temporal_duration,
    "Temporal.Hours": normalize_temporal_hours,
    "Temporal.Milliseconds": normalize_temporal_milliseconds,
    "Temporal.Minutes": normalize_temporal_minutes,
    "Temporal.Month": normalize_temporal_month,
    "Temporal.QuarterYear": normalize_temporal_quarter_year,
    "Temporal.Seconds": normalize_temporal_seconds,
}


DEFAULT_VALIDATE_FUNCTIONS: dict[str, Callable[[str], list]] = {
    "Contact.Email": validate_contact_email,
    "Contact.PhoneNumber": validate_contact_phone_number,
    "Design.Color": validate_design_color,
    "Finance.Money": validate_finance_money,
    "Generic.Int64": validate_generic_int64,
    "Generic.JSON": validate_generic_json,
    "Identity.UUID": validate_identity_uuid,
    "Identity.UserID": validate_identity_user_id,
    "Parable.Permission": validate_parable_permission,
    "Tap.Identifier": validate_tap_identifier,
    "Temporal.Date": validate_temporal_date,
    "Temporal.DateTime": validate_temporal_date_time,
    "Temporal.Days": validate_temporal_days,
    "Temporal.Duration": validate_temporal_duration,
    "Temporal.Hours": validate_temporal_hours,
    "Temporal.Milliseconds": validate_temporal_milliseconds,
    "Temporal.Minutes": validate_temporal_minutes,
    "Temporal.Month": validate_temporal_month,
    "Temporal.QuarterYear": validate_temporal_quarter_year,
    "Temporal.Seconds": validate_temporal_seconds,
}


__all__ = [
    "DEFAULT_PARSE_FUNCTIONS",
    "DEFAULT_NORMALIZE_FUNCTIONS",
    "DEFAULT_VALIDATE_FUNCTIONS",
]
