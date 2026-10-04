package sqlmigrate

import "flag"

// update rewrites this package's golden files (make go-goldens). Every
// golden test in the package reads this one flag.
var update = flag.Bool("update", false, "rewrite golden files")
