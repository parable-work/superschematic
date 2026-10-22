// Executor fixture: a document that exports the names of the environment
// variables it can read, which must hold no credential (D47, amended).
export default { env: Object.keys(process.env).sort() };
