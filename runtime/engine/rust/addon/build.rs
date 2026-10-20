// The link arguments an addon needs on macOS, whose Node-API symbols
// resolve when Node.js or Bun loads it (-undefined dynamic_lookup): only
// the package with the cdylib can pass them.
fn main() {
    napi_build::setup();
}
