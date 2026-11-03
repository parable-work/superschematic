//! Who an engine call acts for: the port of `Principal`, `servicePrincipal`,
//! `checkPrincipal` and `actorOf` from `src/access.ts` and `src/events/log.ts`
//! (D16, D37, D50, D58).
//!
//! A principal has the shape of the HTTP runtime's: the subject the call
//! records as its actor, the permissions the caller holds, its verified
//! claims, and, for a call a service makes (D37), the calling service. A
//! service that brings no end user stands in for one: its subject names the
//! service (`service:<deployable>`) and it holds no permissions.
//!
//! A principal may act for a subject (D77): the person an agent or a
//! service works for, which its writes record as `for` beside `actor`. It
//! adds no permissions; the access policy sees it and may refuse it.
//!
//! [`Principal::system`] is the engine's own principal (D58), which a server
//! publishes its schemas as when it starts: the access policy allows it
//! everything. No request can be it: the mark is private, a principal read
//! from JSON never carries it, and the only way to make one is
//! [`Principal::system`], which a server calls in its own code.

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::errors::EngineError;

/// The caller an engine call acts for.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Principal {
    /// Stable subject identifier, recorded as the actor of what the call
    /// writes.
    pub subject: String,
    pub permissions: Vec<String>,
    /// Verified claims, for the policy's own checks.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub claims: Option<Map<String, Value>>,
    /// The service that made the call (D37): beside the end user it acts
    /// for, or standing in for one.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub service: Option<PrincipalService>,
    /// The subject the caller acts for (D77): the person an agent or a
    /// service works for. It adds no permissions.
    #[serde(rename = "actingFor", default, skip_serializing_if = "Option::is_none")]
    pub acting_for: Option<ActingFor>,
    /// Whether this is the engine's own principal; never set by a request.
    #[serde(skip)]
    system: bool,
}

/// The calling service of a principal (D37).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PrincipalService {
    /// The calling deployable's name.
    pub deployable: String,
    /// The API services it serves.
    pub serves: Vec<String>,
    /// Its credential's subject at its issuer.
    pub subject: String,
    /// True when no end user came with the call: the principal is the
    /// service's own, with no permissions.
    pub stands_in: bool,
}

/// Whom a principal acts for (D77).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ActingFor {
    /// The subject the principal acts for: never its own.
    pub subject: String,
}

/// The subject an `Acting-For` header names (D77), by the one rule every
/// host applies to the header's value, in Rust, TypeScript and Go alike:
/// ASCII whitespace around it is trimmed, and nothing else. `Ok(None)` when
/// it is empty. A value holding a comma is refused, since that is how two
/// `Acting-For` headers read once a host or a proxy folds them into one
/// (RFC 9110, 5.3), and so is one holding a control character or any other
/// whitespace, Unicode's included: what a host's decision approved is then
/// the subject the engine records, byte for byte.
pub fn acting_for_header_subject(value: &str) -> Result<Option<&str>, String> {
    let subject = value.trim_matches(|c: char| c.is_ascii_whitespace());
    if subject.is_empty() {
        return Ok(None);
    }
    if subject.contains(',') {
        return Err(
            "the request carries more than one Acting-For header, or one holding a comma"
                .to_owned(),
        );
    }
    if subject.chars().any(|c| c.is_control() || c.is_whitespace()) {
        return Err(
            "the Acting-For header's subject holds a control or whitespace character".to_owned(),
        );
    }
    Ok(Some(subject))
}

/// The prefix of a service's subject when it stands in for an end user.
pub const SERVICE_SUBJECT_PREFIX: &str = "service:";

/// The subject the engine's own principal records as the actor of what it
/// writes.
pub const SYSTEM_SUBJECT: &str = "system";

impl Principal {
    /// A caller with a subject and the permissions it holds.
    pub fn new<S: Into<String>>(
        subject: impl Into<String>,
        permissions: impl IntoIterator<Item = S>,
    ) -> Self {
        Principal {
            subject: subject.into(),
            permissions: permissions.into_iter().map(Into::into).collect(),
            claims: None,
            service: None,
            acting_for: None,
            system: false,
        }
    }

    /// The principal with its verified claims.
    pub fn with_claims(mut self, claims: Map<String, Value>) -> Self {
        self.claims = Some(claims);
        self
    }

    /// The principal of an end user a service acts for.
    pub fn with_service(mut self, service: PrincipalService) -> Self {
        self.service = Some(service);
        self
    }

    /// The principal acting for a subject (D77).
    pub fn acting_for(mut self, subject: impl Into<String>) -> Self {
        self.acting_for = Some(ActingFor {
            subject: subject.into(),
        });
        self
    }

    /// The subject the principal acts for, if it acts for one (D77).
    pub fn acting_for_subject(&self) -> Option<&str> {
        self.acting_for
            .as_ref()
            .map(|acting| acting.subject.as_str())
    }

    /// The principal of a service that brings no end user and so stands in
    /// for one (D37): its subject is `service:<deployable>` and it holds no
    /// permissions.
    pub fn service(deployable: &str, serves: &[String], subject: &str) -> Self {
        Principal {
            subject: format!("{SERVICE_SUBJECT_PREFIX}{deployable}"),
            permissions: Vec::new(),
            claims: None,
            service: Some(PrincipalService {
                deployable: deployable.to_owned(),
                serves: serves.to_vec(),
                subject: subject.to_owned(),
                stands_in: true,
            }),
            acting_for: None,
            system: false,
        }
    }

    /// The engine's own principal (D58), which the access policy allows
    /// everything and no request can be.
    pub fn system() -> Self {
        Principal {
            subject: SYSTEM_SUBJECT.to_owned(),
            permissions: Vec::new(),
            claims: None,
            service: None,
            acting_for: None,
            system: true,
        }
    }

    /// Whether this is the engine's own principal.
    pub fn is_system(&self) -> bool {
        self.system
    }

    /// Whether this is a service standing in for an end user.
    pub fn stands_in(&self) -> bool {
        self.service
            .as_ref()
            .is_some_and(|service| service.stands_in)
    }
}

/// Refuses a principal with no subject, one whose service has no
/// deployable, a service standing in for an end user that holds
/// permissions, and one acting for a subject (D77) that is empty or its
/// own, or acting for one beside the end user a service brings.
pub fn check_principal(principal: &Principal) -> Result<(), EngineError> {
    if principal.subject.is_empty() {
        return Err(EngineError::invalid_argument(
            "an engine call needs a principal with a subject",
        ));
    }
    if let Some(acting) = &principal.acting_for {
        if acting.subject.is_empty() {
            return Err(EngineError::invalid_argument(
                "a principal's actingFor is { subject }, a subject that is not empty",
            ));
        }
        if acting.subject == principal.subject {
            return Err(EngineError::invalid_argument(format!(
                "{} cannot act for itself: actingFor names another subject",
                principal.subject
            )));
        }
    }
    let Some(service) = &principal.service else {
        return Ok(());
    };
    if service.deployable.is_empty() {
        return Err(EngineError::invalid_argument(
            "a principal's service is { deployable, serves, subject, standsIn }",
        ));
    }
    if service.stands_in && !principal.permissions.is_empty() {
        return Err(EngineError::invalid_argument(format!(
            "service {} stands in for an end user and holds no permissions (D37)",
            service.deployable
        )));
    }
    if !service.stands_in {
        if let Some(acting) = &principal.acting_for {
            // The call already acts for the end user the service brings,
            // who is its actor: a third subject has no place in it (D77).
            return Err(EngineError::invalid_argument(format!(
                "service {} brings end user {}, so the call cannot act for {} too: only a service standing in acts for a subject",
                service.deployable, principal.subject, acting.subject
            )));
        }
    }
    Ok(())
}

/// Who a write records as making it: the principal's subject, for a
/// service's call its deployable, and the subject it acts for (D77).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Actor {
    pub actor: String,
    pub service: Option<String>,
    /// The subject the principal acts for, which an event records as `for`.
    pub for_subject: Option<String>,
}

/// The actor a principal's writes record.
pub fn actor_of(principal: &Principal) -> Actor {
    Actor {
        actor: principal.subject.clone(),
        service: principal
            .service
            .as_ref()
            .map(|service| service.deployable.clone()),
        for_subject: principal.acting_for_subject().map(str::to_owned),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_acting_for_header_is_read_by_one_rule() {
        assert_eq!(acting_for_header_subject(" \talice\r\n"), Ok(Some("alice")));
        assert_eq!(acting_for_header_subject(" \t "), Ok(None));
        assert_eq!(acting_for_header_subject(""), Ok(None));
        for refused in [
            "alice, bob",
            "alice,bob",
            "\u{85}role:admins",
            "role:admins\u{85}",
            "\u{a0}alice",
            "al ice",
            "alice\u{0}",
            "\u{b}alice",
            "\u{2028}alice",
            "\u{3000}alice",
        ] {
            assert!(acting_for_header_subject(refused).is_err(), "{refused:?}");
        }
    }

    #[test]
    fn a_principal_is_checked() {
        assert!(check_principal(&Principal::new("alice", ["orders"])).is_ok());
        assert_eq!(
            check_principal(&Principal::new("", Vec::<String>::new()))
                .unwrap_err()
                .message(),
            "an engine call needs a principal with a subject"
        );
        let mut standing = Principal::service("billing", &["api".into()], "sa@issuer");
        assert_eq!(standing.subject, "service:billing");
        assert!(standing.stands_in());
        assert!(check_principal(&standing).is_ok());
        standing.permissions.push("orders".into());
        assert_eq!(
            check_principal(&standing).unwrap_err().message(),
            "service billing stands in for an end user and holds no permissions (D37)"
        );
        assert_eq!(
            actor_of(&standing),
            Actor {
                actor: "service:billing".into(),
                service: Some("billing".into()),
                for_subject: None,
            }
        );
    }

    #[test]
    fn a_principal_acting_for_a_subject_is_checked() {
        let agent = Principal::new("agent:reviewer", ["tickets"]).acting_for("alice");
        assert!(check_principal(&agent).is_ok());
        assert_eq!(actor_of(&agent).for_subject.as_deref(), Some("alice"));
        assert_eq!(
            check_principal(&Principal::new("agent:reviewer", ["tickets"]).acting_for(""))
                .unwrap_err()
                .message(),
            "a principal's actingFor is { subject }, a subject that is not empty"
        );
        assert_eq!(
            check_principal(&Principal::new("alice", ["tickets"]).acting_for("alice"))
                .unwrap_err()
                .message(),
            "alice cannot act for itself: actingFor names another subject"
        );
        let standing =
            Principal::service("billing", &["api".into()], "sa@issuer").acting_for("alice");
        assert!(check_principal(&standing).is_ok());
        let with_user = Principal::new("bob", ["tickets"])
            .with_service(PrincipalService {
                deployable: "billing".into(),
                serves: vec!["api".into()],
                subject: "sa@issuer".into(),
                stands_in: false,
            })
            .acting_for("alice");
        assert_eq!(
            check_principal(&with_user).unwrap_err().message(),
            "service billing brings end user bob, so the call cannot act for alice too: only a service standing in acts for a subject"
        );
        let written = serde_json::to_value(&agent).unwrap();
        assert_eq!(
            written,
            serde_json::json!({"subject": "agent:reviewer", "permissions": ["tickets"], "actingFor": {"subject": "alice"}})
        );
        let read: Principal = serde_json::from_value(written).unwrap();
        assert_eq!(read, agent);
    }

    #[test]
    fn no_request_is_the_system_principal() {
        assert!(Principal::system().is_system());
        let read: Principal = serde_json::from_value(serde_json::json!({
            "subject": "system",
            "permissions": [],
            "system": true
        }))
        .unwrap();
        assert!(!read.is_system());
        assert_ne!(read, Principal::system());
        let written = serde_json::to_value(Principal::system()).unwrap();
        assert_eq!(
            written,
            serde_json::json!({"subject": "system", "permissions": []})
        );
    }
}
