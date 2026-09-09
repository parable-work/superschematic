package cli

import (
	"github.com/parable-work/superschematic/internal/generator/naming"
)

// resolveNaming loads the naming configuration for a command. The loader and
// the generators receive the returned value as an option; the schema writer
// and schemadeps have no options path, so it is also installed as the
// process-wide active naming for them.
//
// explicitPath (the --naming flag) wins when set; otherwise the file is
// <schemasRoot>/superschematic.toml. A missing file yields the defaults.
func resolveNaming(explicitPath, schemasRoot string) (naming.Naming, error) {
	var (
		names naming.Naming
		err   error
	)
	if explicitPath != "" {
		names, err = naming.LoadFile(explicitPath)
	} else {
		names, err = naming.Load(schemasRoot)
	}
	if err != nil {
		return naming.Naming{}, err
	}
	naming.SetActive(names)
	return names, nil
}
