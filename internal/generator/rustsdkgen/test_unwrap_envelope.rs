use std::collections::BTreeMap;

const ERR_EXPECT_OBJECT: &str =
    "SDK contract error: expected RFC 9457 envelope object with data/meta.requestId in success response";
const ERR_EXPECT_FIELDS: &str =
    "SDK contract error: expected RFC 9457 envelope fields data and meta in success response";
const ERR_EXPECT_REQUEST_ID: &str =
    "SDK contract error: expected RFC 9457 envelope meta.requestId in success response";

#[derive(Clone, Debug, PartialEq)]
enum Value {
    Object(BTreeMap<String, Value>),
    Array(Vec<Value>),
    String(String),
    Number(i64),
    Bool(bool),
    Null,
}

fn object(entries: Vec<(&str, Value)>) -> Value {
    let mut map = BTreeMap::new();
    for (key, value) in entries {
        map.insert(key.to_string(), value);
    }
    Value::Object(map)
}

fn unwrap_envelope(payload: Value) -> Result<Value, String> {
    let object = match payload {
        Value::Object(map) => map,
        _ => return Err(ERR_EXPECT_OBJECT.to_string()),
    };

    let data = match object.get("data") {
        Some(value) => value.clone(),
        None => return Err(ERR_EXPECT_FIELDS.to_string()),
    };

    let meta = match object.get("meta") {
        Some(value) => value,
        None => return Err(ERR_EXPECT_FIELDS.to_string()),
    };

    let meta_object = match meta {
        Value::Object(map) => map,
        _ => return Err(ERR_EXPECT_REQUEST_ID.to_string()),
    };

    if !meta_object.contains_key("requestId") {
        return Err(ERR_EXPECT_REQUEST_ID.to_string());
    }

    Ok(data)
}

#[test]
fn unwrap_envelope_success_cases() {
    let success_cases: Vec<(&str, Value, Value)> = vec![
        (
            "valid envelope object",
            object(vec![
                ("data", object(vec![("id", Value::String("x".to_string()))])),
                (
                    "meta",
                    object(vec![("requestId", Value::String("abc".to_string()))]),
                ),
            ]),
            object(vec![("id", Value::String("x".to_string()))]),
        ),
        (
            "valid envelope null data",
            object(vec![
                ("data", Value::Null),
                (
                    "meta",
                    object(vec![("requestId", Value::String("abc".to_string()))]),
                ),
            ]),
            Value::Null,
        ),
        (
            "valid envelope array data",
            object(vec![
                ("data", Value::Array(vec![Value::Number(1), Value::Number(2)])),
                (
                    "meta",
                    object(vec![("requestId", Value::String("abc".to_string()))]),
                ),
            ]),
            Value::Array(vec![Value::Number(1), Value::Number(2)]),
        ),
        (
            "empty requestId still accepted",
            object(vec![
                ("data", Value::Bool(true)),
                (
                    "meta",
                    object(vec![("requestId", Value::String("".to_string()))]),
                ),
            ]),
            Value::Bool(true),
        ),
    ];

    for (name, payload, expected) in success_cases {
        let actual = unwrap_envelope(payload).expect(name);
        assert_eq!(actual, expected, "{}", name);
    }
}

#[test]
fn unwrap_envelope_error_cases() {
    let error_cases: Vec<(&str, Value, &str)> = vec![
        ("no data key", object(vec![("meta", object(vec![("requestId", Value::String("abc".to_string()))]))]), ERR_EXPECT_FIELDS),
        ("no meta key", object(vec![("data", Value::String("x".to_string()))]), ERR_EXPECT_FIELDS),
        (
            "meta missing requestId",
            object(vec![
                ("data", Value::String("x".to_string())),
                ("meta", object(vec![("other", Value::String("v".to_string()))])),
            ]),
            ERR_EXPECT_REQUEST_ID,
        ),
        (
            "meta not object",
            object(vec![
                ("data", Value::String("x".to_string())),
                ("meta", Value::String("bad".to_string())),
            ]),
            ERR_EXPECT_REQUEST_ID,
        ),
        ("null payload", Value::Null, ERR_EXPECT_OBJECT),
        ("array payload", Value::Array(vec![]), ERR_EXPECT_OBJECT),
        ("number payload", Value::Number(42), ERR_EXPECT_OBJECT),
        ("bool payload", Value::Bool(false), ERR_EXPECT_OBJECT),
    ];

    for (name, payload, expected_error) in error_cases {
        let err = unwrap_envelope(payload).expect_err(name);
        assert_eq!(err, expected_error, "{}", name);
    }
}

#[test]
fn unwrap_envelope_returns_owned_data() {
    let baseline_data = object(vec![("id", Value::String("x".to_string()))]);
    let payload = object(vec![
        ("data", baseline_data.clone()),
        (
            "meta",
            object(vec![("requestId", Value::String("abc".to_string()))]),
        ),
    ]);

    let mut unwrapped = unwrap_envelope(payload).expect("unwrap should succeed");
    if let Value::Object(ref mut data_object) = unwrapped {
        data_object.insert("changed".to_string(), Value::Bool(true));
    } else {
        panic!("expected object payload");
    }

    assert_eq!(
        baseline_data,
        object(vec![("id", Value::String("x".to_string()))]),
    );
}
