// The callee of the telemetry fixture: it answers with the traceparent the
// caller's client span sent.
export default {
  async fetch(request) {
    return Response.json({ traceparent: request.headers.get('traceparent') });
  },
};
