"""Tests for RFC 9457 envelope unwrapping logic.

The _unwrap_envelope function is embedded in the generated Python SDK client
via template. This file tests the algorithm independently.
"""
from __future__ import annotations

import re
from typing import Any

import pytest


class SDKError(Exception):
    """Local SDKError stand-in for unwrap contract tests."""


ERR_EXPECT_OBJECT = (
    "SDK contract error: expected RFC 9457 envelope object with data/meta.requestId in success response"
)
ERR_EXPECT_FIELDS = (
    "SDK contract error: expected RFC 9457 envelope fields data and meta in success response"
)
ERR_EXPECT_REQUEST_ID = (
    "SDK contract error: expected RFC 9457 envelope meta.requestId in success response"
)


def unwrap_envelope(payload: Any) -> Any:
    """Validate strict RFC 9457 envelope responses and extract inner data."""
    if not isinstance(payload, dict):
        raise SDKError(ERR_EXPECT_OBJECT)
    if "data" not in payload or "meta" not in payload:
        raise SDKError(ERR_EXPECT_FIELDS)
    meta = payload["meta"]
    if not isinstance(meta, dict) or "requestId" not in meta:
        raise SDKError(ERR_EXPECT_REQUEST_ID)
    return payload["data"]


@pytest.mark.parametrize(
    ("payload", "expected", "expected_error"),
    [
        pytest.param(
            {"data": {"id": "x"}, "meta": {"requestId": "abc"}},
            {"id": "x"},
            None,
            id="valid_envelope_dict",
        ),
        pytest.param(
            {"data": None, "meta": {"requestId": "abc"}},
            None,
            None,
            id="valid_envelope_none_data",
        ),
        pytest.param(
            {"data": [1, 2, 3], "meta": {"requestId": "abc"}},
            [1, 2, 3],
            None,
            id="valid_envelope_list_data",
        ),
        pytest.param(
            {"data": "hello", "meta": {"requestId": "abc"}},
            "hello",
            None,
            id="valid_envelope_string_data",
        ),
        pytest.param(
            {"data": 42, "meta": {"requestId": "abc"}},
            42,
            None,
            id="valid_envelope_number_data",
        ),
        pytest.param(
            {"data": True, "meta": {"requestId": "abc"}},
            True,
            None,
            id="valid_envelope_bool_data",
        ),
        pytest.param(
            {"result": {"id": "x"}, "meta": {"requestId": "abc"}},
            None,
            ERR_EXPECT_FIELDS,
            id="no_data_key",
        ),
        pytest.param(
            {"data": {"id": "x"}},
            None,
            ERR_EXPECT_FIELDS,
            id="no_meta_key",
        ),
        pytest.param(
            {"data": {"id": "x"}, "meta": {"other": "val"}},
            None,
            ERR_EXPECT_REQUEST_ID,
            id="meta_missing_requestId",
        ),
        pytest.param(
            {"data": {"id": "x"}, "meta": "string"},
            None,
            ERR_EXPECT_REQUEST_ID,
            id="meta_not_dict",
        ),
        pytest.param(
            {"data": {"id": "x"}, "meta": None},
            None,
            ERR_EXPECT_REQUEST_ID,
            id="meta_is_none",
        ),
        pytest.param(
            {"data": {"id": "x"}, "meta": [1]},
            None,
            ERR_EXPECT_REQUEST_ID,
            id="meta_is_list",
        ),
        pytest.param(
            {},
            None,
            ERR_EXPECT_FIELDS,
            id="empty_dict",
        ),
        pytest.param(
            None,
            None,
            ERR_EXPECT_OBJECT,
            id="none_input",
        ),
        pytest.param(
            [1, 2, 3],
            None,
            ERR_EXPECT_OBJECT,
            id="list_input",
        ),
        pytest.param(
            "hello",
            None,
            ERR_EXPECT_OBJECT,
            id="string_input",
        ),
        pytest.param(
            42,
            None,
            ERR_EXPECT_OBJECT,
            id="int_input",
        ),
        pytest.param(
            True,
            None,
            ERR_EXPECT_OBJECT,
            id="bool_input",
        ),
        pytest.param(
            {"error": "bad request"},
            None,
            ERR_EXPECT_FIELDS,
            id="error_response",
        ),
        pytest.param(
            {"error": "Validation failed", "errors": [{"field": "name"}]},
            None,
            ERR_EXPECT_FIELDS,
            id="validation_error",
        ),
        pytest.param(
            {"data": [{"id": "1"}], "total": 5},
            None,
            ERR_EXPECT_FIELDS,
            id="legacy_paginated",
        ),
        pytest.param(
            {"data": {"id": "x"}, "meta": {"requestId": "abc"}, "links": {}},
            {"id": "x"},
            None,
            id="extra_keys",
        ),
        pytest.param(
            {
                "data": {"data": {"id": "inner"}, "meta": {"requestId": "inner-id"}},
                "meta": {"requestId": "outer"},
            },
            {"data": {"id": "inner"}, "meta": {"requestId": "inner-id"}},
            None,
            id="nested_envelope",
        ),
        pytest.param(
            {"data": {"id": "x"}, "meta": {"requestId": ""}},
            {"id": "x"},
            None,
            id="empty_requestId",
        ),
        pytest.param(
            {"data": {"name": "\u65e5\u672c\u8a9e"}, "meta": {"requestId": "abc"}},
            {"name": "\u65e5\u672c\u8a9e"},
            None,
            id="unicode_in_data",
        ),
    ],
)
def test_unwrap_envelope(payload: Any, expected: Any, expected_error: str | None) -> None:
    if expected_error is not None:
        with pytest.raises(SDKError, match=re.escape(expected_error)):
            unwrap_envelope(payload)
        return

    result = unwrap_envelope(payload)
    assert result == expected
