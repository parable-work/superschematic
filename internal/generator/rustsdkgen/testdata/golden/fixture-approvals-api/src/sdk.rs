use crate::client::{ClientConfig, HttpClient};
use crate::errors::SDKError;
use crate::namespaces;

#[derive(Clone)]
pub struct FixtureApprovalsApiSdk {
    client: HttpClient,
    pub task: namespaces::TaskNamespace,
    pub task_admin: namespaces::TaskAdminNamespace,
}

impl FixtureApprovalsApiSdk {
    pub fn new(config: ClientConfig) -> Result<Self, SDKError> {
        let client = HttpClient::new(config)?;
        Ok(Self {
            client: client.clone(),
            task: namespaces::TaskNamespace::new(client.clone()),
            task_admin: namespaces::TaskAdminNamespace::new(client.clone()),
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
