//! The routes over HTTP served by the host in one process (D58's paths,
//! D117): [`Dev::http`] routes a request with the handler's transport-free
//! router (`superschematic_rules_handler::http`), runs it as the caller the
//! host's authentication established, and writes its answer. What a
//! transport must hold open, an event stream and the MCP endpoint, it hands
//! back for the transport to serve ([`Served`]), with what it needs:
//! [`Dev::events_since`] for a stream's frames, and [`Dev::tools`] and
//! [`Dev::call_tool`] for MCP's tools.

use serde_json::{Map, Value};
use superschematic_rules::{Caller, Refusal};
use superschematic_rules_handler::events::EventsQuery;
use superschematic_rules_handler::http::{
    refusal_problem, respond, route, unauthorized, HttpRequest, HttpResponse, Routed,
};
use superschematic_rules_handler::{
    serve_publishing, tools, Context, Handler, QueuedEvent, Request, Store,
};

use crate::Dev;

/// What a request comes to.
#[derive(Debug)]
pub enum Served {
    /// A response, whole.
    Response(HttpResponse),
    /// An event stream the caller may open: the transport sends its frames
    /// ([`Dev::events_since`]).
    Stream { query: EventsQuery, caller: Caller },
    /// A request of the MCP endpoint, which the transport serves over the
    /// caller's tools ([`Dev::tools`], [`Dev::call_tool`]).
    Mcp { caller: Caller },
}

impl<S: Store> Dev<S> {
    /// Serves one request as the caller with the host's id for it, then,
    /// unless the host drains when asked, runs the queues.
    pub fn request_as(
        &mut self,
        caller: &Caller,
        request: &Request,
        request_id: &str,
    ) -> Result<Value, Refusal> {
        let context = Context {
            caller: caller.clone(),
            now: (self.clock)(),
            request: request_id.to_owned(),
            id: uuid::Uuid::new_v4().as_u128(),
        };
        let handler = Handler::new(&self.core, &self.declarations).with_policy(&self.policy);
        let published = serve_publishing(&handler, &mut self.store, request, &context)?;
        // The unit committed: its signals reach the streams open on them.
        self.publish(&published.signals);
        if self.drain == crate::Drain::AfterEachRequest {
            self.run_due()?;
        }
        published.response
    }

    /// Serves one HTTP request: `caller` is who the host's authentication
    /// established (`None` answers 401), and `request_id` the host's id for
    /// the request, which its response carries.
    pub fn http(
        &mut self,
        caller: Option<&Caller>,
        request: &HttpRequest,
        request_id: &str,
    ) -> Served {
        let routed = route(request, request_id);
        if let Routed::Answer(response) = routed {
            return Served::Response(response);
        }
        let Some(caller) = caller else {
            return Served::Response(unauthorized(request_id));
        };
        match routed {
            Routed::Handle { request, answer } => {
                let outcome = self.request_as(caller, &request, request_id);
                Served::Response(respond(&answer, outcome, request_id))
            }
            Routed::Events(query) => {
                let handler =
                    Handler::new(&self.core, &self.declarations).with_policy(&self.policy);
                if let Err(refusal) = handler.admit_events(caller, &query) {
                    return Served::Response(refusal_problem(&refusal, request_id));
                }
                if query.stream {
                    return Served::Stream {
                        query,
                        caller: caller.clone(),
                    };
                }
                let page = self.events_page(caller, &query);
                Served::Response(respond(
                    &superschematic_rules_handler::http::Answer::Data,
                    page,
                    request_id,
                ))
            }
            Routed::Mcp => Served::Mcp {
                caller: caller.clone(),
            },
            Routed::Answer(response) => Served::Response(response),
        }
    }

    /// The last cursor the event queue holds; 0 for none.
    pub fn events_head(&mut self) -> Result<i64, Refusal> {
        Ok(self.store.events(0)?.last().map_or(0, |event| event.seq))
    }

    /// A page of the event queue as the caller may read it.
    pub fn events_page(&mut self, caller: &Caller, query: &EventsQuery) -> Result<Value, Refusal> {
        let head = self.events_head()?;
        let start = query.start(head);
        let events = self.store.events(start)?;
        let handler = Handler::new(&self.core, &self.declarations).with_policy(&self.policy);
        handler.events_page(caller, query, &events, head)
    }

    /// The events a stream sends after a cursor, as the caller may read
    /// them, and the cursor it reads on from: what a transport sends each
    /// time it pulls. The read is asked of the policy again each time.
    pub fn events_since(
        &mut self,
        caller: &Caller,
        query: &EventsQuery,
        cursor: i64,
    ) -> Result<(Vec<QueuedEvent>, i64), Refusal> {
        let events = self.store.events(cursor)?;
        let handler = Handler::new(&self.core, &self.declarations).with_policy(&self.policy);
        handler.admit_events(caller, query)?;
        let (kept, next, _) = handler.events_after(caller, query, &events, usize::MAX);
        Ok((kept.into_iter().cloned().collect(), next.max(cursor)))
    }

    /// The tools the caller may call, each `{tool, call}`: the tool as MCP
    /// lists it, and the call [`Dev::call_tool`] makes of it.
    pub fn tools(&mut self, caller: &Caller) -> Result<Vec<Value>, Refusal> {
        let request_id = uuid::Uuid::new_v4().to_string();
        match self.request_as(caller, &Request::Tools, &request_id)? {
            Value::Array(tools) => Ok(tools),
            _ => Ok(Vec::new()),
        }
    }

    /// Calls a tool by its handle with its arguments, as the caller:
    /// `None` for a tool the caller is not offered.
    pub fn call_tool(
        &mut self,
        caller: &Caller,
        name: &str,
        arguments: &Map<String, Value>,
    ) -> Result<Option<Result<Value, Refusal>>, Refusal> {
        let offered = self.tools(caller)?;
        let Some(call) = offered
            .iter()
            .find(|tool| tool["tool"]["name"] == name)
            .map(|tool| tool["call"].clone())
        else {
            return Ok(None);
        };
        let request = match tools::request_of(&call, arguments) {
            Ok(request) => request,
            Err(refusal) => return Ok(Some(Err(refusal))),
        };
        let request_id = uuid::Uuid::new_v4().to_string();
        Ok(Some(self.request_as(caller, &request, &request_id)))
    }
}
