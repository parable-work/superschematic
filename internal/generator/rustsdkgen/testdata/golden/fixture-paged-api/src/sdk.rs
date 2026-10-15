use crate::client::{ClientConfig, HttpClient};
use crate::errors::SDKError;
use crate::namespaces;

#[derive(Clone)]
pub struct FixturePagedApiSdk {
    client: HttpClient,
    pub catalog: namespaces::CatalogNamespace,
}

impl FixturePagedApiSdk {
    pub fn new(config: ClientConfig) -> Result<Self, SDKError> {
        let client = HttpClient::new(config)?;
        Ok(Self {
            client: client.clone(),
            catalog: namespaces::CatalogNamespace::new(client.clone()),
        })
    }

    pub fn client(&self) -> &HttpClient {
        &self.client
    }
}
