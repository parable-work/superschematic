//! The confirmation an MCP endpoint asks for before it runs a tool whose
//! invocation policy is ask (D93), as the TypeScript runtime's
//! `mcpconfirm.ts` mints and redeems it, token for token. A confirmation is
//! a token the endpoint mints and redeems, statelessly but for a single-use
//! claim:
//!
//! ```text
//! v1.<mode>.<expires>.<nonce>.<binding>.<mac>
//! ```
//!
//! - `mode` is `elicit`, for the `requestState` of a 2026-07-28 client that
//!   asks the person itself (`elicitation/create` in an `input_required`
//!   result), or `token`, for the `confirmationToken` argument of any other
//!   client; one mode's token does not redeem the other's, so a
//!   `requestState` never runs a call the person was not asked about.
//! - `expires` is the Unix second it stops being good.
//! - `nonce` is 16 random bytes, the key the single-use claim takes.
//! - `binding` is the HMAC-SHA-256 of the scope (the API's name), the tool,
//!   the caller and the call's exact arguments as canonical JSON
//!   ([`canonical_json`](super::canonical_json)): a token minted for one
//!   call redeems no other.
//! - `mac` is the HMAC-SHA-256 of everything before it, so no field can be
//!   changed and no token made without the key.
//!
//! Both MACs are keyed with the endpoint's confirmation key, which a stack
//! binds to every instance as one secret, so a token one instance minted
//! redeems on another. The claim that makes it single-use is kept by a
//! [`ConfirmationStore`]: in process by default, so a token is single-use
//! per instance, or in a store the instances share when the deployment
//! supplies one (D116). Every refusal names why: invalid (not a token this
//! key minted, or malformed), expired, mismatch (minted for another call,
//! caller or mode), or used.

use super::json::canonical_object;
use crate::BoxError;
use async_trait::async_trait;
use base64::alphabet;
use base64::engine::general_purpose::{GeneralPurpose, GeneralPurposeConfig};
use base64::engine::DecodePaddingMode;
use base64::Engine as _;
use ring::hmac;
use ring::rand::{SecureRandom, SystemRandom};
use serde_json::{Map, Value};
use std::collections::HashMap;
use std::fmt;
use std::sync::{Arc, Mutex, PoisonError};
use std::time::{SystemTime, UNIX_EPOCH};

/// The `tools/call` argument a confirmation token comes back in, from a
/// client that does not elicit.
pub const CONFIRMATION_ARGUMENT: &str = "confirmationToken";

/// The key of the one input request an `input_required` result asks an
/// eliciting client.
pub const CONFIRMATION_INPUT: &str = "confirm";

/// How long a confirmation is good for unless the endpoint says: ten
/// minutes.
pub const DEFAULT_CONFIRMATION_TTL_SECONDS: u64 = 600;

/// The fewest bytes a confirmation key holds, the HMAC-SHA-256 block of
/// entropy it needs.
pub const MIN_CONFIRMATION_KEY_BYTES: usize = 32;

/// The problem code of a call that waits for the person's confirmation.
pub const CONFIRMATION_REQUIRED: &str = "confirmation_required";

/// The problem code of a call the person declined.
pub const CONFIRMATION_DECLINED: &str = "confirmation_declined";

/// The most claims a [`MemoryConfirmationStore`] holds unless it is built
/// with another bound.
pub const DEFAULT_MAX_CONFIRMATION_CLAIMS: usize = 100_000;

const MAC_DOMAIN: &str = "superschematic mcp confirmation v1\0";
const BINDING_DOMAIN: &str = "superschematic mcp confirmation binding v1\0";
const NONCE_BYTES: usize = 16;
const MAC_BYTES: usize = 32;

// Unpadded base64url, read as the TypeScript runtime's `base64urlDecode`
// reads it: no padding, and the bits past the last byte not checked.
const BASE64URL: GeneralPurpose = GeneralPurpose::new(
    &alphabet::URL_SAFE,
    GeneralPurposeConfig::new()
        .with_encode_padding(false)
        .with_decode_padding_mode(DecodePaddingMode::RequireNone)
        .with_decode_allow_trailing_bits(true),
);

/// How a confirmation comes back: as an eliciting client's `requestState`,
/// or as a token argument.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum ConfirmationMode {
    /// The `requestState` of a 2026-07-28 client that elicits forms.
    Elicit,
    /// The `confirmationToken` argument of any other client.
    Token,
}

impl ConfirmationMode {
    /// The mode as a token names it.
    pub const fn as_str(self) -> &'static str {
        match self {
            Self::Elicit => "elicit",
            Self::Token => "token",
        }
    }

    fn parse(text: &str) -> Option<Self> {
        match text {
            "elicit" => Some(Self::Elicit),
            "token" => Some(Self::Token),
            _ => None,
        }
    }
}

/// Why a token does not redeem.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum ConfirmationRefusal {
    /// Not a token this key minted, or malformed.
    Invalid,
    /// Past the second it stops being good.
    Expired,
    /// Minted for another call, caller, scope or mode.
    Mismatch,
    /// Redeemed already.
    Used,
}

impl ConfirmationRefusal {
    /// The refusal as a problem's `details.refused` names it.
    pub const fn as_str(self) -> &'static str {
        match self {
            Self::Invalid => "invalid",
            Self::Expired => "expired",
            Self::Mismatch => "mismatch",
            Self::Used => "used",
        }
    }
}

impl fmt::Display for ConfirmationRefusal {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// What a confirmation is bound to: the tool, the caller and the call's
/// exact arguments.
#[derive(Clone, Copy, Debug)]
pub struct ConfirmationBinding<'a> {
    /// The tool's handle.
    pub tool: &'a str,
    /// Who the endpoint's request authenticated ([`caller_of`](super::caller_of));
    /// empty for no one.
    pub caller: &'a str,
    /// The arguments without the confirmation token's.
    pub arguments: &'a Map<String, Value>,
}

/// A minted confirmation.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ConfirmationToken {
    /// The token: a `confirmationToken` argument or a `requestState`.
    pub token: String,
    /// When it stops being good, as RFC 3339 UTC to the second.
    pub expires_at: String,
}

/// Where a token's nonce is claimed, so each token redeems once. A server
/// of several instances supplies one backed by something they share for a
/// token to be single-use across them (D93, D116); the interface is the one
/// method the endpoint calls.
#[async_trait]
pub trait ConfirmationStore: Send + Sync {
    /// Takes `id` until `now_ms + ttl_ms` unless it is taken: `true` when
    /// this call took it. Times are Unix milliseconds. An `Err` is a store
    /// that could not be asked, which fails the call with the JSON-RPC
    /// internal error and runs nothing.
    async fn claim(&self, id: &str, ttl_ms: u64, now_ms: u64) -> Result<bool, BoxError>;
}

/// Claims in process memory. Its bound keeps it from growing without end:
/// past it, expired claims are dropped, and while it is full of live ones a
/// claim is refused rather than one forgotten, which would let its token
/// redeem again.
#[derive(Debug)]
pub struct MemoryConfirmationStore {
    claims: Mutex<HashMap<String, u64>>,
    max_claims: usize,
}

impl MemoryConfirmationStore {
    /// A store of at most [`DEFAULT_MAX_CONFIRMATION_CLAIMS`] claims.
    pub fn new() -> Self {
        Self::with_max_claims(DEFAULT_MAX_CONFIRMATION_CLAIMS)
    }

    /// A store of at most `max_claims` claims.
    pub fn with_max_claims(max_claims: usize) -> Self {
        Self {
            claims: Mutex::new(HashMap::new()),
            max_claims,
        }
    }

    /// Takes `id` until `now_ms + ttl_ms` unless it is taken, as
    /// [`ConfirmationStore::claim`] does, without a future to await.
    pub fn claim_now(&self, id: &str, ttl_ms: u64, now_ms: u64) -> bool {
        let mut claims = self.claims.lock().unwrap_or_else(PoisonError::into_inner);
        let held = claims.get(id).copied();
        if held.is_some_and(|until| until > now_ms) {
            return false;
        }
        if held.is_none() && claims.len() >= self.max_claims {
            claims.retain(|_, until| *until > now_ms);
            if claims.len() >= self.max_claims {
                return false;
            }
        }
        claims.insert(id.to_owned(), now_ms.saturating_add(ttl_ms));
        true
    }
}

impl Default for MemoryConfirmationStore {
    fn default() -> Self {
        Self::new()
    }
}

#[async_trait]
impl ConfirmationStore for MemoryConfirmationStore {
    async fn claim(&self, id: &str, ttl_ms: u64, now_ms: u64) -> Result<bool, BoxError> {
        Ok(self.claim_now(id, ttl_ms, now_ms))
    }
}

/// A clock in Unix milliseconds, which a test fixes.
pub type ConfirmationClock = Arc<dyn Fn() -> u64 + Send + Sync>;

/// Why [`Confirmations`] could not be built.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ConfirmationsError {
    /// The key holds fewer than [`MIN_CONFIRMATION_KEY_BYTES`] bytes.
    ShortKey {
        /// The bytes it holds.
        bytes: usize,
    },
    /// The time a confirmation is good for is no positive number of
    /// seconds.
    Ttl {
        /// The seconds given.
        seconds: u64,
    },
}

impl fmt::Display for ConfirmationsError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::ShortKey { bytes } => write!(
                f,
                "the confirmation key holds {bytes} bytes; it needs at least {MIN_CONFIRMATION_KEY_BYTES}, random"
            ),
            Self::Ttl { seconds } => {
                write!(f, "ttlSeconds {seconds} is no positive whole number")
            }
        }
    }
}

impl std::error::Error for ConfirmationsError {}

/// Mints and redeems the confirmations of an MCP endpoint (D93).
#[derive(Clone)]
pub struct Confirmations {
    key: hmac::Key,
    scope: String,
    ttl_seconds: u64,
    store: Arc<dyn ConfirmationStore>,
    clock: ConfirmationClock,
}

// Debug leaves the key out.
impl fmt::Debug for Confirmations {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Confirmations")
            .field("scope", &self.scope)
            .field("ttl_seconds", &self.ttl_seconds)
            .finish_non_exhaustive()
    }
}

impl Confirmations {
    /// Confirmations signed with `key`, the endpoint's confirmation key: at
    /// least [`MIN_CONFIRMATION_KEY_BYTES`] bytes, random, the same on every
    /// instance. They are good for [`DEFAULT_CONFIRMATION_TTL_SECONDS`],
    /// claimed in a [`MemoryConfirmationStore`] of their own, bound to no
    /// scope, and read the system clock, until the methods below say
    /// otherwise.
    pub fn new(key: &[u8]) -> Result<Self, ConfirmationsError> {
        if key.len() < MIN_CONFIRMATION_KEY_BYTES {
            return Err(ConfirmationsError::ShortKey { bytes: key.len() });
        }
        Ok(Self {
            key: hmac::Key::new(hmac::HMAC_SHA256, key),
            scope: String::new(),
            ttl_seconds: DEFAULT_CONFIRMATION_TTL_SECONDS,
            store: Arc::new(MemoryConfirmationStore::new()),
            clock: Arc::new(system_clock),
        })
    }

    /// The confirmations bound to `scope`, what a token is good for beside
    /// its tool: the API's name, so one API's token confirms no other's
    /// call under a shared key.
    #[must_use]
    pub fn with_scope(mut self, scope: impl Into<String>) -> Self {
        self.scope = scope.into();
        self
    }

    /// The confirmations good for `seconds`, which must be positive.
    pub fn with_ttl_seconds(mut self, seconds: u64) -> Result<Self, ConfirmationsError> {
        if seconds == 0 {
            return Err(ConfirmationsError::Ttl { seconds });
        }
        self.ttl_seconds = seconds;
        Ok(self)
    }

    /// The confirmations claimed in `store`, one the instances of a server
    /// share, or two endpoints of one process.
    #[must_use]
    pub fn with_store(mut self, store: Arc<dyn ConfirmationStore>) -> Self {
        self.store = store;
        self
    }

    /// The confirmations on `clock`, for tests.
    #[must_use]
    pub fn with_clock(mut self, clock: ConfirmationClock) -> Self {
        self.clock = clock;
        self
    }

    /// A confirmation of the call `binding` describes, which `mode`
    /// redeems. An `Err` is a system with no random bytes to give.
    pub fn mint(
        &self,
        mode: ConfirmationMode,
        binding: &ConfirmationBinding<'_>,
    ) -> Result<ConfirmationToken, BoxError> {
        let mut nonce = [0u8; NONCE_BYTES];
        SystemRandom::new()
            .fill(&mut nonce)
            .map_err(|_| "no random bytes for a confirmation's nonce")?;
        let expires = ((self.clock)() / 1000).saturating_add(self.ttl_seconds);
        let head = format!(
            "v1.{}.{expires}.{}.{}",
            mode.as_str(),
            BASE64URL.encode(nonce),
            BASE64URL.encode(self.bind(binding))
        );
        let mac = hmac::sign(&self.key, format!("{MAC_DOMAIN}{head}").as_bytes());
        Ok(ConfirmationToken {
            token: format!("{head}.{}", BASE64URL.encode(mac)),
            expires_at: rfc3339_utc(expires),
        })
    }

    /// Redeems `token`, sent back in `mode` for the call `binding`
    /// describes: `None` when it confirms the call, which claims it, or why
    /// not. It checks the MAC (invalid), the mode (mismatch), the expiry
    /// (expired), the binding (mismatch) and the claim (used), in that
    /// order, so only a token that confirms the call is claimed, and one
    /// sent with other arguments stays good for its own. An `Err` is the
    /// store's.
    pub async fn redeem(
        &self,
        token: &Value,
        mode: ConfirmationMode,
        binding: &ConfirmationBinding<'_>,
    ) -> Result<Option<ConfirmationRefusal>, BoxError> {
        let Some(parts) = token.as_str().and_then(Parts::of) else {
            return Ok(Some(ConfirmationRefusal::Invalid));
        };
        let signed = format!("{MAC_DOMAIN}{}", parts.head);
        if hmac::verify(&self.key, signed.as_bytes(), &parts.mac).is_err() {
            return Ok(Some(ConfirmationRefusal::Invalid));
        }
        if parts.mode != mode {
            return Ok(Some(ConfirmationRefusal::Mismatch));
        }
        let now = (self.clock)();
        let expires_ms = parts.expires.saturating_mul(1000);
        if expires_ms <= now {
            return Ok(Some(ConfirmationRefusal::Expired));
        }
        // The token's binding against this call's, compared as a MAC is.
        let bound = BASE64URL.decode(parts.binding).unwrap_or_default();
        if hmac::verify(&self.key, self.binding_text(binding).as_bytes(), &bound).is_err() {
            return Ok(Some(ConfirmationRefusal::Mismatch));
        }
        if !self.store.claim(parts.nonce, expires_ms - now, now).await? {
            return Ok(Some(ConfirmationRefusal::Used));
        }
        Ok(None)
    }

    // bind is the binding's MAC.
    fn bind(&self, binding: &ConfirmationBinding<'_>) -> hmac::Tag {
        hmac::sign(&self.key, self.binding_text(binding).as_bytes())
    }

    // binding_text is what the binding's MAC signs: the scope, the tool,
    // the caller and the arguments, each NUL-terminated but the last.
    fn binding_text(&self, binding: &ConfirmationBinding<'_>) -> String {
        format!(
            "{BINDING_DOMAIN}{}\0{}\0{}\0{}",
            self.scope,
            binding.tool,
            binding.caller,
            canonical_object(binding.arguments)
        )
    }
}

// A token's parts, read without the key.
struct Parts<'a> {
    // Everything before the MAC, which the MAC signs.
    head: &'a str,
    mode: ConfirmationMode,
    expires: u64,
    nonce: &'a str,
    binding: &'a str,
    mac: Vec<u8>,
}

impl<'a> Parts<'a> {
    // of reads a token of six parts whose version is v1, whose mode is one
    // of the two, whose expiry is 1 to 12 digits, whose nonce is 16 bytes
    // and whose MAC is 32.
    fn of(token: &'a str) -> Option<Self> {
        let (head, mac) = token.rsplit_once('.')?;
        let mut fields = head.split('.');
        let (version, mode, expires, nonce, binding) = (
            fields.next()?,
            fields.next()?,
            fields.next()?,
            fields.next()?,
            fields.next()?,
        );
        if fields.next().is_some() || version != "v1" {
            return None;
        }
        let mode = ConfirmationMode::parse(mode)?;
        if expires.is_empty() || expires.len() > 12 || !expires.bytes().all(|b| b.is_ascii_digit())
        {
            return None;
        }
        let expires = expires.parse().ok()?;
        let mac = BASE64URL.decode(mac).ok()?;
        if mac.len() != MAC_BYTES || BASE64URL.decode(nonce).ok()?.len() != NONCE_BYTES {
            return None;
        }
        Some(Self {
            head,
            mode,
            expires,
            nonce,
            binding,
            mac,
        })
    }
}

fn system_clock() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |since| {
            u64::try_from(since.as_millis()).unwrap_or(u64::MAX)
        })
}

// rfc3339_utc is a Unix second in UTC, `2026-10-26T12:01:00Z`: the date by
// Howard Hinnant's `civil_from_days`.
fn rfc3339_utc(seconds: u64) -> String {
    let days = seconds / 86_400;
    let of_day = seconds % 86_400;
    let z = days + 719_468;
    let era = z / 146_097;
    let doe = z % 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = doy - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = yoe + era * 400 + u64::from(month <= 2);
    format!(
        "{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}Z",
        of_day / 3600,
        of_day % 3600 / 60,
        of_day % 60
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_unix_second_is_its_rfc3339_time() {
        assert_eq!(rfc3339_utc(0), "1970-01-01T00:00:00Z");
        assert_eq!(rfc3339_utc(951_782_399), "2000-02-28T23:59:59Z");
        assert_eq!(rfc3339_utc(951_782_400), "2000-02-29T00:00:00Z");
        assert_eq!(rfc3339_utc(1_793_016_060), "2026-10-26T12:01:00Z");
        assert_eq!(rfc3339_utc(4_107_542_400), "2100-03-01T00:00:00Z");
    }

    #[test]
    fn a_token_of_another_shape_has_no_parts() {
        let nonce = BASE64URL.encode([0u8; NONCE_BYTES]);
        let mac = BASE64URL.encode([0u8; MAC_BYTES]);
        let good = format!("v1.token.1700000000.{nonce}.bound.{mac}");
        assert!(Parts::of(&good).is_some());
        for bad in [
            String::new(),
            "v1".to_owned(),
            format!("v2.token.1700000000.{nonce}.bound.{mac}"),
            format!("v1.other.1700000000.{nonce}.bound.{mac}"),
            format!("v1.token..{nonce}.bound.{mac}"),
            format!("v1.token.+1700000000.{nonce}.bound.{mac}"),
            format!("v1.token.1700000000000.{nonce}.bound.{mac}"),
            format!("v1.token.1700000000.{nonce}A.bound.{mac}"),
            format!("v1.token.1700000000.{nonce}.bound.{mac}="),
            format!(
                "v1.token.1700000000.{nonce}.bound.{}",
                &mac[..mac.len() - 2]
            ),
            format!("{good}.more"),
        ] {
            assert!(Parts::of(&bad).is_none(), "{bad}");
        }
    }
}
