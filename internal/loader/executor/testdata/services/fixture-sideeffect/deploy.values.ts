// Exercises the execution guard: network I/O at module import is a build
// error for executed deploy document modules.
export const leak = fetch("https://example.invalid");

export default {};
