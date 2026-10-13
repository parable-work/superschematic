//! Who may do what (D16, D58): the questions the engine asks before every
//! read and write, the port of `Access` in `src/access.ts`.
//!
//! `read` for a schema or its instances and events, `write` to create,
//! update or delete an instance, `define` for a draft, `publish` for a new
//! version; a behavior operation asks `write` or `read` and names itself.
//! `manage` names no schema: it is asked about a namespace itself, with what
//! is done to it (`create`, `archive`, `unarchive`, or `list`).
//!
//! The answers come from a policy. D58 makes the policy data, ordered rules
//! a deployment configures, which milestone 4b reads; until then every
//! engine answers as the TypeScript engine's `allowAll` does, through the
//! crate's [`AccessPolicy`] trait, which that milestone implements. The
//! engine's own principal ([`Principal::system`]) is allowed everything
//! whatever the policy says.

use crate::errors::{EngineError, EngineErrorCode};
use crate::principal::Principal;

/// What a question about one schema asks.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum SchemaAction {
    Read,
    Write,
    Define,
    Publish,
}

impl SchemaAction {
    pub fn as_str(self) -> &'static str {
        match self {
            SchemaAction::Read => "read",
            SchemaAction::Write => "write",
            SchemaAction::Define => "define",
            SchemaAction::Publish => "publish",
        }
    }
}

/// What `manage` asks to do with a namespace.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum NamespaceOperation {
    Create,
    Archive,
    Unarchive,
    List,
}

impl NamespaceOperation {
    pub fn as_str(self) -> &'static str {
        match self {
            NamespaceOperation::Create => "create",
            NamespaceOperation::Archive => "archive",
            NamespaceOperation::Unarchive => "unarchive",
            NamespaceOperation::List => "list",
        }
    }
}

/// One question the engine asks the policy.
#[derive(Clone, Copy, Debug)]
pub enum AccessRequest<'a> {
    /// About a schema of a namespace: its instances, events, drafts and
    /// versions. `operation` names a behavior operation.
    Schema {
        principal: &'a Principal,
        action: SchemaAction,
        namespace: &'a str,
        schema: &'a str,
        operation: Option<&'a str>,
    },
    /// About a namespace itself (`manage`), which names no schema.
    Namespace {
        principal: &'a Principal,
        namespace: &'a str,
        operation: NamespaceOperation,
    },
}

impl AccessRequest<'_> {
    /// Who asks.
    pub fn principal(&self) -> &Principal {
        match self {
            AccessRequest::Schema { principal, .. }
            | AccessRequest::Namespace { principal, .. } => principal,
        }
    }
}

/// Answers the engine's questions: true allows. Milestone 4b implements it
/// with the declarative rules of D58.
pub(crate) trait AccessPolicy: Send + Sync {
    fn allows(&self, request: &AccessRequest<'_>) -> bool;
}

/// Allows everything: the TypeScript engine's `allowAll`, every engine's
/// policy until milestone 4b.
pub(crate) struct AllowAll;

impl AccessPolicy for AllowAll {
    fn allows(&self, _request: &AccessRequest<'_>) -> bool {
        true
    }
}

/// Asks the policy and refuses with `forbidden`.
pub(crate) struct Access {
    policy: Box<dyn AccessPolicy>,
}

impl Access {
    pub fn new(policy: Box<dyn AccessPolicy>) -> Self {
        Access { policy }
    }

    fn ask(&self, request: &AccessRequest<'_>) -> bool {
        request.principal().is_system() || self.policy.allows(request)
    }

    /// Whether the policy allows the action on the schema.
    pub fn allows(
        &self,
        principal: &Principal,
        action: SchemaAction,
        namespace: &str,
        schema: &str,
    ) -> bool {
        self.ask(&AccessRequest::Schema {
            principal,
            action,
            namespace,
            schema,
            operation: None,
        })
    }

    /// Refuses with `forbidden` unless the policy allows the action on the
    /// schema.
    pub fn require(
        &self,
        principal: &Principal,
        action: SchemaAction,
        namespace: &str,
        schema: &str,
    ) -> Result<(), EngineError> {
        if self.allows(principal, action, namespace, schema) {
            return Ok(());
        }
        Err(EngineError::new(
            EngineErrorCode::Forbidden,
            format!(
                "{} may not {} {schema} in namespace {namespace}",
                principal.subject,
                action.as_str()
            ),
        ))
    }

    /// Whether the policy allows the operation on the namespace.
    pub fn allows_manage(
        &self,
        principal: &Principal,
        namespace: &str,
        operation: NamespaceOperation,
    ) -> bool {
        self.ask(&AccessRequest::Namespace {
            principal,
            namespace,
            operation,
        })
    }

    /// Refuses with `forbidden` unless the policy allows the operation on
    /// the namespace.
    pub fn require_manage(
        &self,
        principal: &Principal,
        namespace: &str,
        operation: NamespaceOperation,
    ) -> Result<(), EngineError> {
        if self.allows_manage(principal, namespace, operation) {
            return Ok(());
        }
        Err(EngineError::new(
            EngineErrorCode::Forbidden,
            format!(
                "{} may not {} namespace {namespace}",
                principal.subject,
                operation.as_str()
            ),
        ))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    struct Nothing;

    impl AccessPolicy for Nothing {
        fn allows(&self, _request: &AccessRequest<'_>) -> bool {
            false
        }
    }

    #[test]
    fn a_refusal_is_forbidden_and_the_system_principal_is_never_refused() {
        let access = Access::new(Box::new(Nothing));
        let alice = Principal::new("alice", Vec::<String>::new());
        let refused = access
            .require(&alice, SchemaAction::Define, "default", "Order")
            .unwrap_err();
        assert_eq!(refused.code(), EngineErrorCode::Forbidden);
        assert_eq!(
            refused.message(),
            "alice may not define Order in namespace default"
        );
        assert_eq!(
            access
                .require_manage(&alice, "acme", NamespaceOperation::Archive)
                .unwrap_err()
                .message(),
            "alice may not archive namespace acme"
        );
        assert!(access
            .require(
                &Principal::system(),
                SchemaAction::Publish,
                "default",
                "Order"
            )
            .is_ok());
        assert!(Access::new(Box::new(AllowAll)).allows(
            &alice,
            SchemaAction::Write,
            "default",
            "Order"
        ));
    }
}
