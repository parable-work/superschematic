//! The recorded scenario files' loader (`runtime/engine/testdata/scenarios`,
//! whose README is the format, and the hand-written files beside the
//! transcripts, D104) and the instants they write: [`load`] reads a file and
//! puts each shared value in its places, and [`iso_of`] and [`ms_of_iso`]
//! write and read an instant as the recording wrote it. They came from the
//! engine's test kit, whose replayer read the same files, when the engine
//! went (D103, #587).

use std::path::Path;

use serde_json::{Map, Value};

/// A scenario file, its shared values resolved.
#[derive(Clone, Debug)]
pub struct ScenarioFile {
    pub name: String,
    pub scenarios: Vec<Value>,
}

/// Reads a scenario file and puts each shared value in its places.
pub fn load(path: &Path) -> Result<ScenarioFile, String> {
    let text =
        std::fs::read_to_string(path).map_err(|error| format!("{}: {error}", path.display()))?;
    let mut file: Value =
        serde_json::from_str(&text).map_err(|error| format!("{}: {error}", path.display()))?;
    let shared = file
        .get("shared")
        .and_then(Value::as_object)
        .cloned()
        .unwrap_or_default();
    // A scenario file holds scenarios; an HTTP file holds cases, a case's
    // members a scenario's.
    let mut scenarios = file
        .get_mut("scenarios")
        .and_then(Value::as_array_mut)
        .map(std::mem::take)
        .or_else(|| {
            file.get_mut("cases")
                .and_then(Value::as_array_mut)
                .map(std::mem::take)
        })
        .unwrap_or_default();
    for scenario in &mut scenarios {
        resolve_shared(scenario, &shared)?;
    }
    Ok(ScenarioFile {
        name: file
            .get("name")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned(),
        scenarios,
    })
}

fn resolve_shared(value: &mut Value, shared: &Map<String, Value>) -> Result<(), String> {
    match value {
        Value::Object(members) => {
            if members.len() == 1 {
                if let Some(Value::String(key)) = members.get("$shared") {
                    *value = shared
                        .get(key)
                        .cloned()
                        .ok_or_else(|| format!("no shared value {key}"))?;
                    return Ok(());
                }
            }
            for member in members.values_mut() {
                resolve_shared(member, shared)?;
            }
        }
        Value::Array(items) => {
            for item in items {
                resolve_shared(item, shared)?;
            }
        }
        _ => {}
    }
    Ok(())
}

/// An instant in epoch milliseconds as JavaScript's `toISOString` writes
/// it: `YYYY-MM-DDTHH:MM:SS.mmmZ`.
pub fn iso_of(ms: i64) -> String {
    let days = ms.div_euclid(86_400_000);
    let of_day = ms.rem_euclid(86_400_000);
    // Howard Hinnant's civil_from_days.
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = doy - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = yoe + era * 400 + i64::from(month <= 2);
    format!(
        "{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}.{:03}Z",
        of_day / 3_600_000,
        of_day / 60_000 % 60,
        of_day / 1000 % 60,
        of_day % 1000
    )
}

/// An instant in epoch milliseconds as the version graph's canonical
/// date-time writes it (D32): [`iso_of`]'s, its fraction of a second
/// without trailing zeros and left out when zero.
pub fn canonical_iso_of(ms: i64) -> String {
    let iso = iso_of(ms);
    let (whole, fraction) = iso.split_at(19);
    let digits = fraction.trim_start_matches('.').trim_end_matches('Z');
    let digits = digits.trim_end_matches('0');
    if digits.is_empty() {
        format!("{whole}Z")
    } else {
        format!("{whole}.{digits}Z")
    }
}

/// The instant an RFC 3339 date-time names, as [`iso_of`] or
/// [`canonical_iso_of`] writes it.
pub fn ms_of_iso(text: &str) -> Option<i64> {
    let bytes = text.as_bytes();
    if bytes.len() < 20
        || bytes[4] != b'-'
        || bytes[7] != b'-'
        || bytes[10] != b'T'
        || bytes[13] != b':'
        || bytes[16] != b':'
        || bytes[bytes.len() - 1] != b'Z'
    {
        return None;
    }
    let number = |range: std::ops::Range<usize>| text.get(range)?.parse::<i64>().ok();
    let (year, month, day) = (number(0..4)?, number(5..7)?, number(8..10)?);
    let fraction = text.get(19..text.len() - 1)?;
    let milli = match fraction.strip_prefix('.') {
        None if fraction.is_empty() => 0,
        Some(digits)
            if (1..=3).contains(&digits.len()) && digits.bytes().all(|b| b.is_ascii_digit()) =>
        {
            format!("{digits:0<3}").parse::<i64>().ok()?
        }
        _ => return None,
    };
    let (hour, minute, second) = (number(11..13)?, number(14..16)?, number(17..19)?);
    // Howard Hinnant's days_from_civil.
    let y = if month <= 2 { year - 1 } else { year };
    let era = y.div_euclid(400);
    let yoe = y.rem_euclid(400);
    let mp = if month > 2 { month - 3 } else { month + 9 };
    let doy = (153 * mp + 2) / 5 + day - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    let days = era * 146_097 + doe - 719_468;
    let ms = days * 86_400_000 + hour * 3_600_000 + minute * 60_000 + second * 1000 + milli;
    (iso_of(ms) == text || canonical_iso_of(ms) == text).then_some(ms)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn instants_read_and_write_as_javascript_writes_them() {
        assert_eq!(iso_of(0), "1970-01-01T00:00:00.000Z");
        assert_eq!(iso_of(1_760_000_000_123), "2025-10-09T08:53:20.123Z");
        assert_eq!(
            ms_of_iso("2025-10-09T08:53:20.123Z"),
            Some(1_760_000_000_123)
        );
        assert_eq!(ms_of_iso("2025-13-09T08:53:20.123Z"), None);
        assert_eq!(iso_of(951_782_400_000), "2000-02-29T00:00:00.000Z");
        // The version graph's canonical date-time trims its fraction.
        assert_eq!(
            canonical_iso_of(1_760_000_000_120),
            "2025-10-09T08:53:20.12Z"
        );
        assert_eq!(canonical_iso_of(1_760_000_000_000), "2025-10-09T08:53:20Z");
        assert_eq!(
            ms_of_iso("2025-10-09T08:53:20.12Z"),
            Some(1_760_000_000_120)
        );
        assert_eq!(ms_of_iso("2025-10-09T08:53:20Z"), Some(1_760_000_000_000));
        assert_eq!(
            ms_of_iso("2025-10-09T08:53:20.120Z"),
            Some(1_760_000_000_120)
        );
        assert_eq!(ms_of_iso("2025-10-09T08:53:20.1200Z"), None);
        assert_eq!(ms_of_iso("2025-10-09T08:53:20.Z"), None);
    }

    #[test]
    fn a_file_reads_with_its_shared_values_in_their_places() {
        let dir = std::env::temp_dir().join(format!("rules-testkit-load-{}", std::process::id()));
        std::fs::create_dir_all(&dir).expect("a temporary directory");
        let path = dir.join("shared.json");
        let file = json!({
            "name": "shared",
            "shared": {"doc": {"kind": "General", "name": "Note"}},
            "scenarios": [{"name": "a", "steps": [{"args": [{"$shared": "doc"}, {"$shared": "doc", "x": 1}]}]}]
        });
        std::fs::write(&path, file.to_string()).expect("write the file");
        let loaded = load(&path).expect("the file loads");
        assert_eq!(loaded.name, "shared");
        assert_eq!(
            loaded.scenarios[0]["steps"][0]["args"],
            json!([{"kind": "General", "name": "Note"}, {"$shared": "doc", "x": 1}])
        );
        std::fs::write(
            &path,
            json!({"scenarios": [{"$shared": "gone"}]}).to_string(),
        )
        .expect("write the file");
        assert_eq!(load(&path).unwrap_err(), "no shared value gone");
        // An HTTP file's cases read as scenarios.
        std::fs::write(
            &path,
            json!({"name": "h", "cases": [{"name": "c"}]}).to_string(),
        )
        .expect("write the file");
        assert_eq!(load(&path).expect("the file loads").scenarios.len(), 1);
        std::fs::remove_dir_all(&dir).expect("remove the directory");
    }
}
