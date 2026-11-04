use crate::client::{ClientConfig, HttpClient};
use crate::errors::SDKError;
use crate::namespaces;

#[derive(Clone)]
pub struct FixtureAgentsApiSdk {
    client: HttpClient,
    pub account: namespaces::AccountNamespace,
    pub job: namespaces::JobNamespace,
    pub session: namespaces::SessionNamespace,
}

impl FixtureAgentsApiSdk {
    pub fn new(config: ClientConfig) -> Result<Self, SDKError> {
        let client = HttpClient::new(config)?;
        Ok(Self {
            client: client.clone(),
            account: namespaces::AccountNamespace::new(client.clone()),
            job: namespaces::JobNamespace::new(client.clone()),
            session: namespaces::SessionNamespace::new(client.clone()),
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
