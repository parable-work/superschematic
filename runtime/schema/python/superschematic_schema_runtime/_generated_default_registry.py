# @generated; do not edit
# Default scalar registries for the schema runtime: one row per scalar in
# the superscalar Go package this repository pins (superscalar.pin).
# Regenerate with: go run ./internal/tools/scalarcatalog

from __future__ import annotations

from typing import Callable

from superscalar import SCALAR_ID_BY_CANONICAL, ValidationError, _native
from superscalar import (
    normalize_contact_email,
    normalize_contact_phone_number,
    normalize_design_color,
    normalize_temporal_duration,
    parse_finance_money,
    parse_generic_int64,
    parse_generic_string_map,
    parse_identity_user_id,
    parse_identity_uuid,
    parse_temporal_date_time,
    parse_temporal_days,
    parse_temporal_duration,
    parse_temporal_hours,
    parse_temporal_milliseconds,
    parse_temporal_minutes,
    parse_temporal_seconds,
)


def _validator(canonical_name: str) -> Callable[[str], list]:
    scalar_id = SCALAR_ID_BY_CANONICAL[canonical_name]

    def validate(value: str) -> list:
        try:
            _native.validate(scalar_id, value)
        except ValueError as exc:
            return [ValidationError(validator="custom", message=str(exc))]
        return []

    return validate


DEFAULT_PARSE_FUNCTIONS: dict[str, Callable[[str], str]] = {
    "Finance.Money": parse_finance_money,
    "Generic.Int64": parse_generic_int64,
    "Generic.StringMap": parse_generic_string_map,
    "Identity.UUID": parse_identity_uuid,
    "Identity.UserID": parse_identity_user_id,
    "Temporal.DateTime": parse_temporal_date_time,
    "Temporal.Days": parse_temporal_days,
    "Temporal.Duration": parse_temporal_duration,
    "Temporal.Hours": parse_temporal_hours,
    "Temporal.Milliseconds": parse_temporal_milliseconds,
    "Temporal.Minutes": parse_temporal_minutes,
    "Temporal.Seconds": parse_temporal_seconds,
}


DEFAULT_NORMALIZE_FUNCTIONS: dict[str, Callable[[str], str]] = {
    "Contact.Email": normalize_contact_email,
    "Contact.PhoneNumber": normalize_contact_phone_number,
    "Design.Color": normalize_design_color,
    "Temporal.Duration": normalize_temporal_duration,
}


DEFAULT_VALIDATE_FUNCTIONS: dict[str, Callable[[str], list]] = {
    "AgentSkill.Name": _validator("AgentSkill.Name"),
    "Auth.JWT": _validator("Auth.JWT"),
    "Auth.Password": _validator("Auth.Password"),
    "Contact.Email": _validator("Contact.Email"),
    "Contact.PhoneNumber": _validator("Contact.PhoneNumber"),
    "Crypto.RSAPrivateKey": _validator("Crypto.RSAPrivateKey"),
    "Crypto.RSAPublicKey": _validator("Crypto.RSAPublicKey"),
    "Crypto.SHA256": _validator("Crypto.SHA256"),
    "Design.Color": _validator("Design.Color"),
    "Embedding.Vector": _validator("Embedding.Vector"),
    "File.SizeBytes": _validator("File.SizeBytes"),
    "Finance.Money": _validator("Finance.Money"),
    "Generic.Int64": _validator("Generic.Int64"),
    "Generic.JSON": _validator("Generic.JSON"),
    "Generic.Probability": _validator("Generic.Probability"),
    "Generic.StringMap": _validator("Generic.StringMap"),
    "Geo.Location": _validator("Geo.Location"),
    "Git.PathPattern": _validator("Git.PathPattern"),
    "Identity.Name": _validator("Identity.Name"),
    "Identity.Slug": _validator("Identity.Slug"),
    "Identity.UUID": _validator("Identity.UUID"),
    "Identity.UserID": _validator("Identity.UserID"),
    "Localization.Locale": _validator("Localization.Locale"),
    "Network.DnsLabel": _validator("Network.DnsLabel"),
    "Network.DomainName": _validator("Network.DomainName"),
    "Network.IpAddress": _validator("Network.IpAddress"),
    "Network.Uri": _validator("Network.Uri"),
    "Network.Url": _validator("Network.Url"),
    "Ordering.Rank": _validator("Ordering.Rank"),
    "Temporal.CronExpression": _validator("Temporal.CronExpression"),
    "Temporal.Date": _validator("Temporal.Date"),
    "Temporal.DateTime": _validator("Temporal.DateTime"),
    "Temporal.Days": _validator("Temporal.Days"),
    "Temporal.Duration": _validator("Temporal.Duration"),
    "Temporal.Hours": _validator("Temporal.Hours"),
    "Temporal.Milliseconds": _validator("Temporal.Milliseconds"),
    "Temporal.Minutes": _validator("Temporal.Minutes"),
    "Temporal.Month": _validator("Temporal.Month"),
    "Temporal.Quarter": _validator("Temporal.Quarter"),
    "Temporal.QuarterYear": _validator("Temporal.QuarterYear"),
    "Temporal.RecurrenceRule": _validator("Temporal.RecurrenceRule"),
    "Temporal.Seconds": _validator("Temporal.Seconds"),
    "Temporal.Time": _validator("Temporal.Time"),
    "Temporal.TimeZone": _validator("Temporal.TimeZone"),
    "Temporal.Year": _validator("Temporal.Year"),
    "Text.Markdown": _validator("Text.Markdown"),
    "Text.Sql": _validator("Text.Sql"),
    "Version.SemVer": _validator("Version.SemVer"),
}


__all__ = [
    "DEFAULT_PARSE_FUNCTIONS",
    "DEFAULT_NORMALIZE_FUNCTIONS",
    "DEFAULT_VALIDATE_FUNCTIONS",
]
