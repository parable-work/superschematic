#[allow(unused_imports)]
use serde::Serialize;


use crate::client::HttpClient;
use crate::errors::SDKError;
use crate::runtime;
use crate::types;

#[derive(Clone)]
pub struct ProblemNamespace {
    /// The operations of the Schedules Problem implements (D103).
    pub schedules: ProblemSchedulesOperations,
    /// The operations of Problem's status, a Workflow (D103).
    pub status: ProblemStatusOperations,
}

impl ProblemNamespace {
    pub(crate) fn new(
        client: HttpClient,
    ) -> Self {
        Self {
            schedules: ProblemSchedulesOperations::new(client.clone()),
            status: ProblemStatusOperations::new(client),
        }
    }
}

/// The operations of the Schedules Problem implements (D103).
#[derive(Clone)]
pub struct ProblemSchedulesOperations {
    client: HttpClient,
}

impl ProblemSchedulesOperations {
    pub(crate) fn new(client: HttpClient) -> Self {
        Self { client }
    }
    /// Finishes a live schedule, clearing its deadline, so it no longer counts toward max; it stays, finished, for its latest run's report and a read. A finished one is answered unchanged.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn cancel_schedule(
        &self,
        id: types::IdentityUUID,
        input: types::ProblemSchedulesCancelScheduleInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::ProblemSchedulesCancelScheduleResult, SDKError> {
        let path = &format!("/api/problem/{}/schedules/cancel-schedule", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("ProblemSchedulesCancelScheduleInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Lists the row's schedules by name in code point order, live and finished; with name, only that one.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn list_schedules(
        &self,
        id: types::IdentityUUID,
        input: types::ProblemSchedulesListSchedulesInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::ProblemSchedulesListSchedulesResult, SDKError> {
        let path = &format!("/api/problem/{}/schedules/list-schedules", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("ProblemSchedulesListSchedulesInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Pauses an active schedule: it keeps its next run and the retry of its latest one, and its deadline is cleared until it resumes. A paused one is answered unchanged.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn pause_schedule(
        &self,
        id: types::IdentityUUID,
        input: types::ProblemSchedulesPauseScheduleInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::ProblemSchedulesPauseScheduleResult, SDKError> {
        let path = &format!("/api/problem/{}/schedules/pause-schedule", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("ProblemSchedulesPauseScheduleInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Records the outcome of a schedule's latest run, one of the config's outcomes. The config's retry outcome tries the run again afterMs on, as its next attempt, unless the next regular run comes first; another outcome cancels a retry armed for it. A report of an older run, of a replaced schedule's latest run, or of an attempt other than the latest, or a repeat of the outcome recorded, is answered unchanged.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn report_run(
        &self,
        id: types::IdentityUUID,
        input: types::ProblemSchedulesReportRunInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::ProblemSchedulesReportRunResult, SDKError> {
        let path = &format!("/api/problem/{}/schedules/report-run", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("ProblemSchedulesReportRunInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Resumes a paused schedule, arming its next run, or the retry of its latest, again: one past its time falls due at once and fires once. An active one is answered unchanged; a row in a state endOn names resumes none.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn resume_schedule(
        &self,
        id: types::IdentityUUID,
        input: types::ProblemSchedulesResumeScheduleInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::ProblemSchedulesResumeScheduleResult, SDKError> {
        let path = &format!("/api/problem/{}/schedules/resume-schedule", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("ProblemSchedulesResumeScheduleInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
    /// Sets a schedule of the row by its name: exactly one of after, every and cron, of a kind the config's kinds take, within minMs and maxMs, a cron line read in timeZone. A live schedule of the name is replaced and its runs start again; one with the same kind, spec and time zone is answered unchanged. A new name needs room under max, and a row in a state endOn names takes none.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn set_schedule(
        &self,
        id: types::IdentityUUID,
        input: types::ProblemSchedulesSetScheduleInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::ProblemSchedulesSetScheduleResult, SDKError> {
        let path = &format!("/api/problem/{}/schedules/set-schedule", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("ProblemSchedulesSetScheduleInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}

/// The operations of Problem's status, a Workflow (D103).
#[derive(Clone)]
pub struct ProblemStatusOperations {
    client: HttpClient,
}

impl ProblemStatusOperations {
    pub(crate) fn new(client: HttpClient) -> Self {
        Self { client }
    }
    /// Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it.
    ///
    /// Requires authentication.
    ///
    /// - `options.timeout_ms`: optional per-request timeout override.
    /// - `options.request_hook`: optional request builder hook.
    pub async fn transition(
        &self,
        id: types::IdentityUUID,
        input: types::ProblemStatusTransitionInput,
        options: Option<&runtime::RequestOptions>,
    ) -> Result<types::ProblemStatusTransitionResult, SDKError> {
        let path = &format!("/api/problem/{}/status/transition", runtime::path_segment(&id));
        let input_value = serde_json::to_value(&input)?;
        runtime::validate_input("ProblemStatusTransitionInput", &input_value)?;
        let query_params: Vec<(String, String)> = Vec::new();
        let body = Some(input_value);
        self.client
            .request_json("POST", path, &query_params, body, options)
            .await
    }
}
