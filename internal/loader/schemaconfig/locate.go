package schemaconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Form is the format a service's config is written in.
type Form string

const (
	// FormTS is schema.config.ts, which the TypeScript frontend reads.
	FormTS Form = "ts"
	// FormJSON is schema.config.json.
	FormJSON Form = "json"
	// FormYAML is schema.config.yaml.
	FormYAML Form = "yaml"
)

// FileName is the config file of the form: schema.config.ts for FormTS.
func (f Form) FileName() string { return "schema.config." + string(f) }

// Forms are the config forms, in the order FileNames lists them.
var Forms = []Form{FormTS, FormJSON, FormYAML}

// FileNames are the names a service's config file may take, one per form.
var FileNames = []string{FormTS.FileName(), FormJSON.FileName(), FormYAML.FileName()}

// Locate finds the config of the service in dir. ok is false, with no
// error, when dir holds none: it is no service. A directory that holds
// more than one form is an error, since no reader could tell which one
// the service means; the error names each file found.
func Locate(dir string) (path string, form Form, ok bool, err error) {
	var found []Form
	for _, f := range Forms {
		info, statErr := os.Stat(filepath.Join(dir, f.FileName()))
		switch {
		case statErr == nil && !info.IsDir():
			found = append(found, f)
		case statErr != nil && !errors.Is(statErr, fs.ErrNotExist) && !errors.Is(statErr, syscall.ENOTDIR):
			return "", "", false, statErr
		}
	}
	switch len(found) {
	case 0:
		return "", "", false, nil
	case 1:
		return filepath.Join(dir, found[0].FileName()), found[0], true, nil
	}
	names := make([]string, len(found))
	for i, f := range found {
		names[i] = f.FileName()
	}
	return "", "", false, fmt.Errorf("%s holds %s: a service has one config; remove all but one", dir, strings.Join(names, " and "))
}
