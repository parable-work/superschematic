//! An operation's input and result, as the generated handlers take and
//! answer them: the input type parsed by its generated `parse_<type>` with
//! undeclared top-level keys refused, the body object scalar arguments are
//! read from, and the result in the success envelope.

use axum::response::{IntoResponse, Response};
use axum::Json;
use serde::Serialize;
use serde_json::{Map, Value};

use crate::schema::{ParseError, UnknownFields, ValidationErrors};
use crate::{error_response, wrap_envelope, ApiError};

/// An input type's generated `parse_<type>`.
pub type InputParse<T> = fn(Value, UnknownFields) -> Result<T, ParseError>;

/// The operation's optional input: `None` without a body, else the body
/// parsed as the input type.
pub fn input<T>(body: Option<Value>, parse: InputParse<T>) -> Result<Option<T>, ApiError> {
    body.map(|body| parse(body, UnknownFields::Refuse).map_err(input_refusal))
        .transpose()
}

/// The operation's required input: 400 without a body, else the body parsed
/// as the input type.
pub fn required_input<T>(body: Option<Value>, parse: InputParse<T>) -> Result<T, ApiError> {
    let body = body.ok_or_else(|| ApiError::bad_request("Request body is required"))?;
    parse(body, UnknownFields::Refuse).map_err(input_refusal)
}

/// The 400 of a body the input type refuses, with the same detail as the
/// TypeScript server's. `details.reason` says which step refused it; a key
/// the type does not declare and a field that breaks a rule are also in the
/// top-level `errors` member the Go server writes and every SDK reads,
/// keyed by path.
pub fn input_refusal(err: ParseError) -> ApiError {
    let (reason, errors) = match err {
        ParseError::NotAnObject => ("expected an object".to_owned(), None),
        ParseError::UnknownFields(keys) => {
            let mut errors = ValidationErrors::new();
            for key in &keys {
                errors.add(key.as_str(), "unknown", "unknown field");
            }
            (format!("unknown fields: {}", keys.join(", ")), Some(errors))
        }
        ParseError::Invalid(errors) => ("validation failed".to_owned(), Some(errors)),
        ParseError::Decode(_) => ("does not match the declared type".to_owned(), None),
    };
    let mut details = Map::new();
    details.insert("location".to_owned(), Value::from("body"));
    details.insert("reason".to_owned(), Value::from(reason));
    let refusal = ApiError::bad_request("Request body does not match the declared input")
        .with_details(Value::Object(details));
    match errors.map(|errors| serde_json::to_value(&errors)) {
        Some(Ok(errors)) => refusal.with_errors(errors),
        _ => refusal,
    }
}

/// The body object a non-GET operation's scalar arguments are read from:
/// `None` without a body, which is 400 when an argument is required, and
/// 400 for a body that is not a JSON object.
pub fn body_fields(
    body: Option<&Value>,
    required: bool,
) -> Result<Option<&Map<String, Value>>, ApiError> {
    match body {
        None if required => Err(ApiError::bad_request("Request body is required")),
        None => Ok(None),
        Some(Value::Object(fields)) => Ok(Some(fields)),
        Some(_) => Err(ApiError::bad_request("Request body must be a JSON object")),
    }
}

/// What a handler answers for its implementation's result: the result's
/// JSON in the success envelope, or the refusal's problem. A result that
/// does not serialize is 500.
pub fn operation_response<T: Serialize>(
    result: Result<T, ApiError>,
    request_id: Option<&str>,
) -> Response {
    match result.and_then(|data| {
        serde_json::to_value(data).map_err(|err| {
            ApiError::internal(format!("The response could not be serialized: {err}"))
        })
    }) {
        Ok(data) => Json(wrap_envelope(data, request_id)).into_response(),
        Err(err) => error_response(err),
    }
}

#[cfg(test)]
mod tests {
    use axum::http::StatusCode;
    use serde_json::json;

    use super::*;

    fn parse_named(value: Value, unknown: UnknownFields) -> Result<String, ParseError> {
        let object = value.as_object().ok_or(ParseError::NotAnObject)?;
        if unknown == UnknownFields::Refuse {
            let extra: Vec<String> = object
                .keys()
                .filter(|key| *key != "name")
                .cloned()
                .collect();
            if !extra.is_empty() {
                return Err(ParseError::UnknownFields(extra));
            }
        }
        match object.get("name").and_then(Value::as_str) {
            Some(name) => Ok(name.to_owned()),
            None => {
                let mut errors = ValidationErrors::new();
                errors.add("name", "required", "required field");
                Err(ParseError::Invalid(errors))
            }
        }
    }

    #[test]
    fn an_input_is_parsed_with_unknown_keys_refused() {
        assert_eq!(
            required_input(Some(json!({"name": "a"})), parse_named).unwrap(),
            "a"
        );
        assert_eq!(input(None, parse_named).unwrap(), None);

        let err = required_input(None, parse_named).unwrap_err();
        assert_eq!(
            (err.status, err.message.as_str()),
            (StatusCode::BAD_REQUEST, "Request body is required")
        );

        let err = required_input(Some(json!({"name": "a", "x": 1})), parse_named).unwrap_err();
        assert_eq!(
            err.message,
            "Request body does not match the declared input"
        );
        assert_eq!(
            err.details.as_deref(),
            Some(&json!({"location": "body", "reason": "unknown fields: x"}))
        );
        assert_eq!(
            err.errors.as_deref(),
            Some(&json!({"x": [{"validator": "unknown", "message": "unknown field"}]}))
        );

        let err = input(Some(json!({})), parse_named).unwrap_err();
        assert_eq!(err.details.unwrap()["reason"], "validation failed");
        assert_eq!(
            err.errors.as_deref(),
            Some(&json!({"name": [{"validator": "required", "message": "required field"}]}))
        );

        let err = input(Some(json!([])), parse_named).unwrap_err();
        assert!(err.errors.is_none());
    }

    #[test]
    fn scalar_arguments_need_an_object_body() {
        assert_eq!(body_fields(None, false).unwrap(), None);
        assert_eq!(
            body_fields(None, true).unwrap_err().message,
            "Request body is required"
        );
        assert_eq!(
            body_fields(Some(&json!([1])), false).unwrap_err().message,
            "Request body must be a JSON object"
        );
        assert!(body_fields(Some(&json!({"a": 1})), true).unwrap().is_some());
    }
}
