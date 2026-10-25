package topcoat

import "github.com/parable-work/superschematic/registry"

// moneyOf reports whether a display component of the crate shows an amount
// in its currency (D44, amended), so views.rs formats it with money_text
// and Cargo.toml depends on the scalar crate for each currency's minor
// units (WritesMoney). Money is not formatted yet: every amount shows as a
// number, and the build log says so for a crate with display components.
func moneyOf(c registry.GenerateContext, schemas schemaSet, out *crate) (bool, error) {
	if len(out.Views) == 0 {
		return false, nil
	}
	c.Logf("  - topcoat: views.rs shows every amount as a number: money is not formatted by its currency yet\n")
	return false, nil
}
