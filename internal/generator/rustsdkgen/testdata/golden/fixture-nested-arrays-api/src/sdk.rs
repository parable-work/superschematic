use crate::client::{ClientConfig, HttpClient};
use crate::errors::SDKError;
use crate::namespaces;

#[derive(Clone)]
pub struct FixtureNestedArraysApiSdk {
    client: HttpClient,
    pub grid: namespaces::GridNamespace,
}

impl FixtureNestedArraysApiSdk {
    pub fn new(config: ClientConfig) -> Result<Self, SDKError> {
        let client = HttpClient::new(config)?;
        Ok(Self {
            client: client.clone(),
            grid: namespaces::GridNamespace::new(client.clone()),
        })
    }

    pub fn client(&self) -> &HttpClient {
        &self.client
    }
}
