// Minimal schema content: the fixture exists to exercise authDb wired to an
// imported service sentinel in schema.config.ts.
export enum AuthProbeStatus {
  Ok = "ok",
  Denied = "denied"
}
