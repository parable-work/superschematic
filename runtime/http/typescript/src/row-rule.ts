/*
A row rule's member that holds a list of ids (#735): a rule names a member
such as watchers, a list of UUIDs, on the row or at the end of a path, and
reaches the rows whose list holds the caller's id. The repositories hold
the condition for the rule alone: a filter's member under HOLDS, a symbol,
which no request's filter, decoded from JSON or a query string, can name,
so no SDK, OpenAPI document or MCP tool describes it.
*/

/** The operation of a list member's filter that keeps the rows whose list holds the id it names: the row rule's, never a request's. */
export const HOLDS: unique symbol = Symbol.for('superschematic.rowRule.holds');
