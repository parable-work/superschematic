use serde::{Deserialize, Serialize};
use std::fmt;

/// The runtime interfaces (D159).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, PartialOrd, Ord, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum RuntimeInterface {
    /// A credential provider ([`CredentialProvider`](super::CredentialProvider)).
    Credentials,
    /// A channel adapter ([`ChannelAdapter`](super::ChannelAdapter)).
    Channel,
    /// A sandbox provider ([`SandboxProvider`](super::SandboxProvider)).
    Sandbox,
    /// A model provider ([`ModelProvider`](super::ModelProvider)).
    Model,
    /// A file processor ([`FileProcessor`](super::FileProcessor)).
    Files,
}

impl RuntimeInterface {
    /// The interfaces, in the contracts' order.
    pub const ALL: [RuntimeInterface; 5] = [
        RuntimeInterface::Credentials,
        RuntimeInterface::Channel,
        RuntimeInterface::Sandbox,
        RuntimeInterface::Model,
        RuntimeInterface::Files,
    ];

    /// The interface's name: `credentials`, `channel`, `sandbox`, `model`
    /// or `files`.
    pub fn as_str(self) -> &'static str {
        match self {
            RuntimeInterface::Credentials => "credentials",
            RuntimeInterface::Channel => "channel",
            RuntimeInterface::Sandbox => "sandbox",
            RuntimeInterface::Model => "model",
            RuntimeInterface::Files => "files",
        }
    }

    /// The interface named `name`, or none for a name that is no
    /// interface.
    pub fn parse(name: &str) -> Option<Self> {
        Self::ALL.into_iter().find(|i| i.as_str() == name)
    }

    /// The interface's contract.
    pub fn contract(self) -> &'static RuntimeContract {
        &RUNTIME_CONTRACTS[self as usize]
    }
}

impl fmt::Display for RuntimeInterface {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// What a provider of one interface implements: the operations core
/// calls, each one it must and each one it may. In Rust each is a method
/// of the interface's trait, the optional ones with a default that answers
/// [`Unsupported`](super::Unsupported); the names are the contract's, as
/// the build and a manifest name them.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct RuntimeContract {
    pub interface: RuntimeInterface,
    /// What a message calls a provider of it.
    pub title: &'static str,
    /// The operations every provider implements.
    pub required: &'static [&'static str],
    /// The operations a provider may implement, and lists in its manifest
    /// when it does.
    #[serde(skip_serializing_if = "<[_]>::is_empty")]
    pub optional: &'static [&'static str],
}

/// The five contracts, in the Go runtime's order.
pub const RUNTIME_CONTRACTS: [RuntimeContract; 5] = [
    RuntimeContract {
        interface: RuntimeInterface::Credentials,
        title: "credential provider",
        required: &["list", "resolve"],
        optional: &[],
    },
    RuntimeContract {
        interface: RuntimeInterface::Channel,
        title: "channel adapter",
        required: &["send", "health"],
        optional: &["status", "inbound"],
    },
    RuntimeContract {
        interface: RuntimeInterface::Sandbox,
        title: "sandbox provider",
        required: &["start", "cancel", "status", "cost"],
        optional: &[],
    },
    RuntimeContract {
        interface: RuntimeInterface::Model,
        title: "model provider",
        required: &["complete"],
        optional: &[],
    },
    RuntimeContract {
        interface: RuntimeInterface::Files,
        title: "file processor",
        required: &["extractText", "preview"],
        optional: &[],
    },
];

/// The contract of the interface named `name`, or none for a name that is
/// no interface.
pub fn contract_of(name: &str) -> Option<&'static RuntimeContract> {
    RuntimeInterface::parse(name).map(RuntimeInterface::contract)
}

/// The subject of a provider's principal: `extension:<name>`.
pub fn extension_subject(provider: &str) -> String {
    format!("extension:{provider}")
}

/// The reserved setting of a provider that calls the app with a key of its
/// own (#744): slot `SMS` reads it from `SMS_CALLER_KEY`, a
/// `Secret<string>`, and a request whose Bearer token is the key runs as
/// the provider, with its role alone ([`Providers::extension_keys`],
/// [`ExtensionAuthenticator`]).
///
/// [`Providers::extension_keys`]: super::Providers::extension_keys
/// [`ExtensionAuthenticator`]: super::ExtensionAuthenticator
pub const CALLER_KEY: &str = "CALLER_KEY";

/// The config field a provider in `slot` reads its setting `key` from:
/// `SMS` and `URL` give `SMS_URL`.
pub fn provider_config_field(slot: &str, key: &str) -> String {
    format!("{slot}_{key}")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_contracts_are_the_ones_the_build_checks_a_provider_against() {
        let file: serde_json::Value =
            serde_json::from_str(include_str!("../../../testdata/runtime_contracts.json")).unwrap();
        assert_eq!(
            serde_json::to_value(RUNTIME_CONTRACTS).unwrap(),
            file["contracts"]
        );
    }

    #[test]
    fn an_interface_is_named_as_its_contract_names_it() {
        for interface in RuntimeInterface::ALL {
            assert_eq!(interface.contract().interface, interface);
            assert_eq!(RuntimeInterface::parse(interface.as_str()), Some(interface));
        }
        assert!(contract_of("payments").is_none());
        assert_eq!(extension_subject("relay-a"), "extension:relay-a");
        assert_eq!(provider_config_field("SMS", "URL"), "SMS_URL");
    }
}
