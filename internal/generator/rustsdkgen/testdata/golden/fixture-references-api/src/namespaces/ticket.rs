#[allow(unused_imports)]
use serde::Serialize;


use crate::client::HttpClient;
use crate::errors::SDKError;
use crate::runtime;
use crate::types;

#[derive(Clone)]
pub struct TicketNamespace {
    client: HttpClient,
}

impl TicketNamespace {
    pub(crate) fn new(
        client: HttpClient,
    ) -> Self {
        Self {
            client,
        }
    }
    /// Makes the row point at another row, of any table it may point at, with a role (none unless given), once per target and role; answers the reference, the one there when it was there.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn add_reference(
        &self,
        id: types::IdentityUUID,
        input: types::RowReferenceInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::RowReference, SDKError> {
        let path = &format!("/api/ticket/{}/references", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("RowReferenceInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Lists the row's ready files, oldest first.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn attachments(
        &self,
        id: types::IdentityUUID,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<Vec<types::RowAttachment>, SDKError> {
        let path = &format!("/api/ticket/{}/files", runtime::path_segment(&id));
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("GET", path, &query_params, None, options)
            .await
    }
    /// Lists the rows that point at the row, across every table, through a reference or a ref block of a message, each one the caller can read, most recent first. Only the owner of a row whose audience is not everyone hears how many rows it cannot see point at it.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn backlinks(
        &self,
        id: types::IdentityUUID,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::RowBacklinks, SDKError> {
        let path = &format!("/api/ticket/{}/backlinks", runtime::path_segment(&id));
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("GET", path, &query_params, None, options)
            .await
    }
    /// Starts an upload of a file to the row: a pending file, held to the table's bounds with the row's other files and uploads, and the URL the browser puts the file's bytes to, straight to the bucket, before it expires.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn begin_upload(
        &self,
        id: types::IdentityUUID,
        input: types::RowAttachmentBeginInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::RowAttachmentBegun, SDKError> {
        let path = &format!("/api/ticket/{}/files", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("RowAttachmentBeginInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Checks an upload's bytes in the bucket against the size and SHA-256 its begin declared, and makes the file ready; a file whose bytes differ is removed.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn complete_upload(
        &self,
        id: types::IdentityUUID,
        file: String,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::RowAttachment, SDKError> {
        let path = &format!("/api/ticket/{}/files/{}/complete", runtime::path_segment(&id), runtime::path_segment(&file));
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("POST", path, &query_params, None, options)
            .await
    }
    /// Signs a URL that downloads one of the row's files until it expires, and answers the file, whose preview this first view asks.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn download(
        &self,
        id: types::IdentityUUID,
        file: String,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::RowAttachmentDownload, SDKError> {
        let path = &format!("/api/ticket/{}/files/{}", runtime::path_segment(&id), runtime::path_segment(&file));
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("GET", path, &query_params, None, options)
            .await
    }
    /// Files one of the row's files onto another row, which may be of another table, with no second upload: a copy that names the same object, or with move the file itself. When the table keeps references that may point at the other row, the row then points at it with the role (filed unless given), so the other row lists it among its backlinks.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn file_attachment(
        &self,
        id: types::IdentityUUID,
        file: String,
        input: types::RowAttachmentFileInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::RowAttachment, SDKError> {
        let path = &format!("/api/ticket/{}/files/{}/file", runtime::path_segment(&id), runtime::path_segment(&file));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("RowAttachmentFileInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// get_ticket endpoint.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn get_ticket(
        &self,
        id: types::IdentityUUID,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::Ticket, SDKError> {
        let path = &format!("/api/tickets/{}", runtime::path_segment(&id));
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("GET", path, &query_params, None, options)
            .await
    }
    /// Lists the references the row holds, oldest first, each to a row the caller can read.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn references(
        &self,
        id: types::IdentityUUID,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<Vec<types::RowReference>, SDKError> {
        let path = &format!("/api/ticket/{}/references", runtime::path_segment(&id));
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("GET", path, &query_params, None, options)
            .await
    }
    /// Removes one of the row's files; its object is released once no file names it.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn remove_attachment(
        &self,
        id: types::IdentityUUID,
        file: String,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<bool, SDKError> {
        let path = &format!("/api/ticket/{}/files/{}", runtime::path_segment(&id), runtime::path_segment(&file));
        let query_params: Vec<(String, String)> = Vec::new();
        self.client
            .request_json("DELETE", path, &query_params, None, options)
            .await
    }
    /// Stops the row pointing at another row with the role, or with any role when none is given; answers how many references it removed.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn remove_reference(
        &self,
        id: types::IdentityUUID,
        input: types::RowReferenceInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::RowReferencesRemoved, SDKError> {
        let path = &format!("/api/ticket/{}/references/remove", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("RowReferenceInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}
