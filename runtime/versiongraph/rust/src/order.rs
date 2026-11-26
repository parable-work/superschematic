//! Order keys (D137): `order_keys` makes string keys that sort between two
//! others, and `arrange` gives the fewest rows of one parent new keys so its
//! children read in the order wanted.
//!
//! An order key is one or more characters of `0-9A-Za-z`, compared by its
//! bytes. Read as the digits of a base 62 fraction (`0` is 0, `z` is 61),
//! a key that is numerically below another is below it by its bytes too, so
//! the keys made here are fractions strictly between their bounds' values.
//! They never end in `0`. Two keys with one value (`A` and `A0`) have no
//! such fraction between them.

use std::cmp::Ordering;
use std::collections::{BTreeMap, BTreeSet};

use serde_json::{Map, Value};

use crate::descriptor::Graph;
use crate::error::Error;
use crate::tree::{self, Order, Row, Tree};

const ALPHABET: &[u8; 62] = b"0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
const BASE: u64 = 62;

/// The most keys one `order_keys` call makes.
pub const MAX_COUNT: u64 = 1_000_000;

/// A key's digits, without trailing zeros: its value as a fraction.
fn digits(key: &str) -> Vec<u32> {
    let mut digits: Vec<u32> = key
        .bytes()
        .map(|b| match b {
            b'0'..=b'9' => u32::from(b - b'0'),
            b'A'..=b'Z' => u32::from(b - b'A') + 10,
            _ => u32::from(b - b'a') + 36,
        })
        .collect();
    while digits.last() == Some(&0) {
        digits.pop();
    }
    digits
}

/// `value` cut to `length` digits, zeros added where it is shorter.
fn floor(value: &[u32], length: usize) -> Vec<u32> {
    let mut out: Vec<u32> = value.iter().copied().take(length).collect();
    out.resize(length, 0);
    out
}

/// `a + b`, both big-endian base 62, as wide as `a`. The caller knows the
/// sum fits.
fn add(a: &[u32], b: &[u32]) -> Vec<u32> {
    let mut out = a.to_vec();
    let mut carry = 0;
    let mut addend = b.iter().rev();
    for digit in out.iter_mut().rev() {
        let sum = *digit + addend.next().copied().unwrap_or(0) + carry;
        *digit = sum % BASE as u32;
        carry = sum / BASE as u32;
    }
    out
}

/// `a - b`, both big-endian base 62 of one width, where `a >= b`.
fn subtract(a: &[u32], b: &[u32]) -> Vec<u32> {
    let mut out = a.to_vec();
    let mut borrow = 0;
    for (digit, less) in out.iter_mut().rev().zip(b.iter().rev()) {
        let take = less + borrow;
        if *digit >= take {
            *digit -= take;
            borrow = 0;
        } else {
            *digit = *digit + BASE as u32 - take;
            borrow = 1;
        }
    }
    out
}

/// `floor(a * by / over)`, with `by <= over`, as wide as `a`.
fn scale(a: &[u32], by: u64, over: u64) -> Vec<u32> {
    let mut product = vec![0u64; a.len()];
    let mut carry = 0u64;
    for (slot, digit) in product.iter_mut().rev().zip(a.iter().rev()) {
        let value = u64::from(*digit) * by + carry;
        *slot = value % BASE;
        carry = value / BASE;
    }
    // The carry out of the top digit is below `by`, and so below `over`: it
    // is the division's first remainder.
    let mut remainder = carry;
    let mut out = Vec::with_capacity(a.len());
    for digit in product {
        let value = remainder * BASE + digit;
        out.push((value / over) as u32);
        remainder = value % over;
    }
    out
}

/// Whether a big-endian base 62 number is at least `least`.
fn at_least(value: &[u32], least: u64) -> bool {
    let mut total = 0u64;
    for digit in value {
        total = total.saturating_mul(BASE).saturating_add(u64::from(*digit));
        if total >= least {
            return true;
        }
    }
    total >= least
}

fn render(digits: &[u32]) -> String {
    let mut end = digits.len();
    while end > 0 && digits[end - 1] == 0 {
        end -= 1;
    }
    digits[..end]
        .iter()
        .map(|digit| char::from(ALPHABET[*digit as usize]))
        .collect()
}

/// `count` keys strictly between `before` and `after` (the start and the
/// end when absent), evenly spread, each as short as it can be: the keys
/// are the `count` fractions that divide the gap evenly among those of the
/// fewest digits that hold that many, less their trailing zeros. `None`
/// when no fraction lies between the bounds: `after` is not above `before`,
/// or is `before` followed by zeros.
pub fn keys(before: Option<&str>, after: Option<&str>, count: usize) -> Option<Vec<String>> {
    if count == 0 {
        return Some(Vec::new());
    }
    let low = before.map(digits).unwrap_or_default();
    let high = after.map(digits);
    if let Some(high) = &high {
        if low.cmp(high) != Ordering::Less {
            return None;
        }
    }
    let wanted = count as u64;
    let mut length = 1;
    loop {
        // One more digit in front holds the end, 1, and a carry.
        let mut bottom = vec![0];
        bottom.extend(floor(&low, length));
        let top = match &high {
            None => {
                let mut top = vec![0; length + 1];
                top[0] = 1;
                top
            }
            Some(high) => {
                let mut top = vec![0];
                top.extend(floor(high, length));
                if high.len() > length {
                    let one = [1];
                    top = add(&top, &one);
                }
                top
            }
        };
        let gap = subtract(&top, &bottom);
        if at_least(&gap, wanted + 1) {
            return Some(
                (1..=wanted)
                    .map(|i| render(&add(&bottom, &scale(&gap, i, wanted + 1))[1..]))
                    .collect(),
            );
        }
        length += 1;
    }
}

/// `order_keys`: `{"before"?, "after"?, "count"}` to `{"keys"}`.
pub fn order_keys(mut members: Map<String, Value>) -> Result<Value, Error> {
    let before = bound(&mut members, "before")?;
    let after = bound(&mut members, "after")?;
    let count = match members.get("count").and_then(Value::as_u64) {
        Some(count) if count <= MAX_COUNT => count as usize,
        _ => {
            return Err(Error::request(format!(
                "order_keys: count must be an integer from 0 to {MAX_COUNT}"
            )))
        }
    };
    let Some(keys) = keys(before.as_deref(), after.as_deref(), count) else {
        let name = |bound: Option<String>, absent: &str| match bound {
            Some(key) => format!("{key:?}"),
            None => absent.to_owned(),
        };
        return Err(Error::new(
            "no_order_key",
            format!(
                "order_keys: no key is made between {} and {}: the second must be above the first by more than trailing zeros",
                name(before, "the start"),
                name(after, "the end")
            ),
        ));
    };
    Ok(serde_json::json!({ "keys": keys }))
}

fn bound(members: &mut Map<String, Value>, member: &str) -> Result<Option<String>, Error> {
    match members.remove(member) {
        None | Some(Value::Null) => Ok(None),
        Some(Value::String(key)) if tree::is_order_key(&key) => Ok(Some(key)),
        Some(_) => Err(Error::request(format!(
            "order_keys: {member} must be an order key, one or more characters of 0-9A-Za-z"
        ))),
    }
}

/// `arrange`: `{"descriptor", "tree", "kind", "parent"?, "keys"}` to
/// `{"rows"}`. `keys` are the entity keys of one parent's live children of
/// `kind` in the order wanted; `rows` are the children that need a new
/// order key to read so, each as an engine writes it, in that order. The
/// longest run of children already in order keeps its keys.
pub fn arrange(
    graph: &Graph,
    tree: &Tree,
    kind_name: &Value,
    parent: Option<&Value>,
    wanted: &Value,
) -> Result<Value, Error> {
    let Some(kind_name) = kind_name.as_str() else {
        return Err(Error::request("arrange: kind must be a string"));
    };
    let Some(index) = graph.kind_index(kind_name) else {
        return Err(Error::new(
            "unknown_kind",
            format!("arrange: kind {kind_name:?} is not in the descriptor"),
        ));
    };
    let kind = &graph.kinds[index];
    let column = match &kind.order {
        Some(column) if kind.order_is_key => column,
        Some(column) => {
            return Err(Error::new(
                "order_not_a_key",
                format!(
                    "arrange: kind {kind_name:?} orders by {column:?}, an integer; arrange gives string order keys, so save each row's order instead"
                ),
            ))
        }
        None => {
            return Err(Error::new(
                "order_not_a_key",
                format!("arrange: kind {kind_name:?} has no order column"),
            ))
        }
    };
    let parent = match parent {
        None | Some(Value::Null) => None,
        Some(Value::String(parent)) if !parent.is_empty() => {
            if kind.parent.is_none() {
                return Err(Error::new(
                    "invalid_arrangement",
                    format!("arrange: kind {kind_name:?} has no parent, so its rows have none"),
                ));
            }
            Some(parent.as_str())
        }
        Some(_) => {
            return Err(Error::request(
                "arrange: parent must be a non-empty string or null",
            ))
        }
    };
    let Value::Array(wanted) = wanted else {
        return Err(Error::request("arrange: keys must be an array of strings"));
    };
    let children: BTreeMap<&str, &Row> = tree.kinds[index]
        .iter()
        .filter(|row| !row.deleted && row.parent.as_deref() == parent)
        .map(|row| (row.key.as_str(), row))
        .collect();
    let mut rows: Vec<&Row> = Vec::with_capacity(wanted.len());
    let mut seen = BTreeSet::new();
    for key in wanted {
        let Some(key) = key.as_str() else {
            return Err(Error::request("arrange: keys must be an array of strings"));
        };
        let Some(row) = children.get(key) else {
            return Err(Error::new(
                "invalid_arrangement",
                format!("arrange: {kind_name} {key:?} is not a live child of the parent"),
            ));
        };
        if !seen.insert(key) {
            return Err(Error::new(
                "invalid_arrangement",
                format!("arrange: {kind_name} {key:?} is listed more than once"),
            ));
        }
        rows.push(row);
    }
    if let Some(missing) = children.keys().find(|key| !seen.contains(*key)) {
        return Err(Error::new(
            "invalid_arrangement",
            format!(
                "arrange: the parent's child {kind_name} {missing:?} is not listed; keys are every live child of the parent"
            ),
        ));
    }

    let orders: Vec<&str> = rows
        .iter()
        .map(|row| match &row.order {
            Order::Key(key) => key.as_str(),
            Order::Integer(_) => "",
        })
        .collect();
    let mut kept = longest_run(&rows);
    let mut out = Vec::new();
    let mut at = 0;
    while at < rows.len() {
        if kept[at] {
            at += 1;
            continue;
        }
        // A gap: the rows from here to the next kept one take keys between
        // their kept neighbors'. When no key lies between those, the
        // neighbor after gives its key up and the gap runs on to the next.
        let before = at.checked_sub(1).map(|i| orders[i]);
        let mut end = at;
        let new_keys = loop {
            while end < rows.len() && !kept[end] {
                end += 1;
            }
            let after = (end < rows.len()).then(|| orders[end]);
            match keys(before, after, end - at) {
                Some(keys) => break keys,
                None => kept[end] = false,
            }
        };
        for (row, key) in rows[at..end].iter().zip(new_keys) {
            if row.order == Order::Key(key.clone()) {
                continue;
            }
            let mut value = row.filled(kind);
            value.insert(column.clone(), Value::String(key));
            out.push(Value::Object(value));
        }
        at = end;
    }
    Ok(serde_json::json!({ "rows": out }))
}

/// The rows of the longest run already in order: the longest subsequence
/// that rises strictly by order key and then entity key, as rows sort. Of
/// several it is the one a patience sort finds, whose last row is the
/// lowest.
fn longest_run(rows: &[&Row]) -> Vec<bool> {
    let rank = |i: usize| (&rows[i].order, rows[i].key.as_str());
    let mut tails: Vec<usize> = Vec::new();
    let mut previous: Vec<Option<usize>> = Vec::with_capacity(rows.len());
    for i in 0..rows.len() {
        let at = tails.partition_point(|&tail| rank(tail) < rank(i));
        previous.push(at.checked_sub(1).map(|before| tails[before]));
        if at == tails.len() {
            tails.push(i);
        } else {
            tails[at] = i;
        }
    }
    let mut kept = vec![false; rows.len()];
    let mut next = tails.last().copied();
    while let Some(i) = next {
        kept[i] = true;
        next = previous[i];
    }
    kept
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn keys_divide_a_gap_evenly() {
        assert_eq!(keys(None, None, 1).unwrap(), ["V"]);
        assert_eq!(keys(None, None, 3).unwrap(), ["F", "V", "k"]);
        assert_eq!(keys(Some("A"), Some("C"), 1).unwrap(), ["B"]);
        assert_eq!(keys(Some("V"), Some("W"), 1).unwrap(), ["VV"]);
        assert_eq!(keys(Some("z"), None, 1).unwrap(), ["zV"]);
        assert_eq!(keys(None, Some("1"), 1).unwrap(), ["0V"]);
        assert_eq!(keys(None, None, 61).unwrap().len(), 61);
        assert_eq!(keys(None, None, 62).unwrap()[0], "0z");
    }

    #[test]
    fn keys_sort_strictly_between_their_bounds() {
        let bounds = [
            (None, None),
            (Some("A"), Some("A1")),
            (Some("A0"), Some("A01")),
            (Some("zzz"), None),
            (None, Some("001")),
            (Some("Vabc"), Some("Vabd")),
            (Some("9"), Some("A")),
        ];
        for (before, after) in bounds {
            for count in [1, 2, 7, 61, 62, 500] {
                let made = keys(before, after, count).unwrap();
                assert_eq!(made.len(), count);
                let mut last = before.unwrap_or("").to_owned();
                for key in &made {
                    assert!(tree::is_order_key(key));
                    assert!(!key.ends_with('0'));
                    assert!(*key > last, "{key} above {last}");
                    last = key.clone();
                }
                if let Some(after) = after {
                    assert!(last.as_str() < after);
                }
            }
        }
    }

    #[test]
    fn keys_refuse_bounds_with_nothing_between() {
        assert_eq!(keys(Some("A"), Some("A"), 1), None);
        assert_eq!(keys(Some("B"), Some("A"), 1), None);
        assert_eq!(keys(Some("A"), Some("A00"), 1), None);
        assert_eq!(keys(None, Some("0"), 1), None);
        assert_eq!(keys(Some("A"), Some("A"), 0).unwrap().len(), 0);
    }
}
