package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
)

// addNamingFlag registers --naming, the naming config file a command reads,
// into path. def, in parentheses, says which file it reads without one.
func addNamingFlag(cmd *cobra.Command, path *string, def string) {
	cmd.Flags().StringVar(path, "naming", "", "naming config file "+def)
}

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

// openNaming resolves the naming of the schemas root (resolveNaming) and
// the registry the app assembles with it, the start of every command that
// reads a project.
func (a *app) openNaming(explicitPath, schemasRoot string) (naming.Naming, *registry.Registry, error) {
	names, err := resolveNaming(explicitPath, schemasRoot)
	if err != nil {
		return naming.Naming{}, nil, err
	}
	reg, err := a.resolveRegistry(names)
	if err != nil {
		return naming.Naming{}, nil, err
	}
	return names, reg, nil
}

// flagRegistry is the registry of a command that reads no project: it
// assembles with the built-in names, or with the file --naming names
// (explicitPath) when it is set.
func (a *app) flagRegistry(explicitPath string) (*registry.Registry, error) {
	names := naming.Default()
	if explicitPath != "" {
		var err error
		if names, err = resolveNaming(explicitPath, ""); err != nil {
			return nil, err
		}
	}
	return a.resolveRegistry(names)
}

// openedService is a service directory a command opened: its absolute
// path, its schemas root (the services directory's parent), and the naming
// and registry resolved there.
type openedService struct {
	dir         string
	schemasRoot string
	names       naming.Naming
	reg         *registry.Registry
}

// openService opens the service directory servicePath, which must exist,
// with the naming of its schemas root, or of the file --naming names
// (namingPath).
func (a *app) openService(servicePath, namingPath string) (*openedService, error) {
	dir, err := filepath.Abs(servicePath)
	if err != nil {
		return nil, fmt.Errorf("resolving service path: %w", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("service directory not found: %s", servicePath)
	}
	s := &openedService{dir: dir, schemasRoot: filepath.Dir(filepath.Dir(dir))}
	if s.names, s.reg, err = a.openNaming(namingPath, s.schemasRoot); err != nil {
		return nil, err
	}
	return s, nil
}
