use crate::client::{ClientConfig, HttpClient};
use crate::errors::SDKError;
use crate::namespaces;

#[derive(Clone)]
pub struct FixtureSeatsApiSdk {
    client: HttpClient,
    pub problem: namespaces::ProblemNamespace,
    pub sandbox: namespaces::SandboxNamespace,
}

impl FixtureSeatsApiSdk {
    pub fn new(config: ClientConfig) -> Result<Self, SDKError> {
        let client = HttpClient::new(config)?;
        Ok(Self {
            client: client.clone(),
            problem: namespaces::ProblemNamespace::new(client.clone()),
            sandbox: namespaces::SandboxNamespace::new(client.clone()),
        })
    }

    pub fn client(&self) -> &HttpClient {
        &self.client
    }
    pub fn set_token(&self, token: impl Into<String>) {
        self.client.set_token(token);
    }

    pub fn clear_token(&self) {
        self.client.clear_token();
    }
}
