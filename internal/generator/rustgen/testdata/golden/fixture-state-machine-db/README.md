# Generated Rust Types: fixture-state-machine-db

This crate contains Rust types generated from the `fixture-state-machine-db` schema.

## Generated Files

- `Cargo.toml` - crate manifest
- `src/lib.rs` - module exports
- `src/scalars.rs` - scalar aliases
- `src/enums.rs` - enum definitions
- `src/types.rs` - object/input structs and imported type aliases
- `src/state_machines.rs` - for each field a `@stateMachine` governs (D118): `<TYPE>_<FIELD>_TERMINAL`, the states no move leaves, and `<type>_can_transition_<field>(from, to)`, whether a move is one the machine lists, as the table's guard decides it in the database

- `schemas/<Type>.json` - the schema IR of each `@jsonField` payload type and the definitions it reaches

## Notes

- This crate is generated. Do not edit manually.
- Re-generate with `superschematic build` for the target schema.
