package loader

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

const compositeDefaultSuffix = ".platform-default.json"

type compositeDefaultFile struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

func loadCompositeDefaults(servicePath string, schema *ir.Schema, externalEnums map[string]*ir.EnumDef) error {
	files, err := compositeDefaultFiles(servicePath)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	if schema.CompositeDefaults == nil {
		schema.CompositeDefaults = make(map[string]*ir.CompositeDefaultDef)
	}

	var errs []error
	for _, rel := range files {
		def, err := readCompositeDefault(filepath.Join(servicePath, filepath.FromSlash(rel)), rel)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, exists := schema.CompositeDefaults[def.Type]; exists {
			errs = append(errs, fmt.Errorf("%s: duplicate platform default for type %q", rel, def.Type))
			continue
		}
		if _, exists := schema.Types[def.Type]; !exists {
			errs = append(errs, fmt.Errorf("%s: platform default target type %q is not declared in this schema", rel, def.Type))
			continue
		}

		var value any
		decoder := json.NewDecoder(bytes.NewReader(def.Value))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			errs = append(errs, fmt.Errorf("%s: decode platform default value: %w", rel, err))
			continue
		}
		if err := validateCompositeValue(schema, externalEnums, ir.TypeRef{Name: def.Type}, value, def.Type); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", rel, err))
			continue
		}
		canonical, err := json.Marshal(value)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: canonicalize platform default: %w", rel, err))
			continue
		}
		schema.CompositeDefaults[def.Type] = &ir.CompositeDefaultDef{
			Type:          def.Type,
			CanonicalJSON: string(canonical),
			Owner:         rel,
		}
	}
	return errors.Join(errs...)
}

func compositeDefaultFiles(servicePath string) ([]string, error) {
	srcDir := filepath.Join(servicePath, "src")
	var files []string
	err := filepath.WalkDir(srcDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), compositeDefaultSuffix) {
			return nil
		}
		rel, err := filepath.Rel(servicePath, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("walking %s: %w", srcDir, err)
	}
	sort.Strings(files)
	return files, nil
}

func readCompositeDefault(path, source string) (*compositeDefaultFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: read platform default: %w", source, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var def compositeDefaultFile
	if err := decoder.Decode(&def); err != nil {
		return nil, fmt.Errorf("%s: decode platform default: %w", source, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("%s: decode platform default: %w", source, err)
	}
	if strings.TrimSpace(def.Type) == "" {
		return nil, fmt.Errorf("%s: platform default requires a non-empty type", source)
	}
	if len(def.Value) == 0 {
		return nil, fmt.Errorf("%s: platform default requires a value", source)
	}
	return &def, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("multiple JSON values are not allowed")
}

func validateCompositeValue(schema *ir.Schema, externalEnums map[string]*ir.EnumDef, ref ir.TypeRef, value any, path string) error {
	if ref.IsArray {
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", path)
		}
		// The element of T[][] is T[]: an inner list, which is never null.
		elementRef := ref
		if ref.IsArrayOfArrays {
			elementRef.IsArrayOfArrays = false
		} else {
			elementRef.IsArray = false
		}
		for i, item := range items {
			if err := validateCompositeValue(schema, externalEnums, elementRef, item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		return nil
	}

	switch ref.Name {
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be a string", path)
		}
		return nil
	case "number":
		if _, ok := value.(json.Number); !ok {
			return fmt.Errorf("%s must be a number", path)
		}
		return nil
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", path)
		}
		return nil
	}

	if scalar, ok := schema.Scalars[ref.Name]; ok {
		return validateCompositeScalar(scalar, value, path)
	}
	if enum, ok := schema.Enums[ref.Name]; ok {
		return validateCompositeEnum(enum, value, path)
	}
	if enum, ok := externalEnums[ref.Name]; ok {
		return validateCompositeEnum(enum, value, path)
	}
	if union, ok := schema.Unions[ref.Name]; ok {
		return validateCompositeUnion(schema, externalEnums, union, value, path)
	}
	if object, ok := schema.Types[ref.Name]; ok {
		return validateCompositeObject(schema, externalEnums, object, value, path)
	}
	return fmt.Errorf("%s references unsupported type %q", path, ref.Name)
}

func validateCompositeScalar(scalar *ir.ScalarDef, value any, path string) error {
	switch scalar.LanguagePrimitive {
	case ir.LanguageString:
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", path)
		}
		if scalar.MinLength > 0 && len(text) < scalar.MinLength {
			return fmt.Errorf("%s must contain at least %d characters", path, scalar.MinLength)
		}
		if scalar.MaxLength > 0 && len(text) > scalar.MaxLength {
			return fmt.Errorf("%s must contain at most %d characters", path, scalar.MaxLength)
		}
		if scalar.Pattern != "" {
			pattern, err := regexp.Compile(scalar.Pattern)
			if err != nil {
				return fmt.Errorf("%s scalar %q has invalid pattern: %w", path, scalar.Name, err)
			}
			if !pattern.MatchString(text) {
				return fmt.Errorf("%s does not match scalar %q", path, scalar.Name)
			}
		}
	case ir.LanguageNumber:
		number, ok := value.(json.Number)
		if !ok {
			return fmt.Errorf("%s must be a number", path)
		}
		parsed, err := number.Float64()
		if err != nil {
			return fmt.Errorf("%s must be a valid number", path)
		}
		if strings.Contains(strings.ToLower(scalar.Primitive), "int") {
			if _, err := strconv.ParseInt(number.String(), 10, 64); err != nil {
				return fmt.Errorf("%s must be an integer", path)
			}
		}
		if scalar.Minimum != nil && parsed < float64(*scalar.Minimum) {
			return fmt.Errorf("%s must be at least %d", path, *scalar.Minimum)
		}
		if scalar.Maximum != nil && parsed > float64(*scalar.Maximum) {
			return fmt.Errorf("%s must be at most %d", path, *scalar.Maximum)
		}
	case ir.LanguageBoolean:
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", path)
		}
	case ir.LanguageObject:
		if _, objectOK := value.(map[string]any); !objectOK {
			if _, arrayOK := value.([]any); !arrayOK {
				return fmt.Errorf("%s must be an object or array", path)
			}
		}
	}
	return nil
}

func validateCompositeEnum(enum *ir.EnumDef, value any, path string) error {
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("%s must be a string enum value", path)
	}
	for _, member := range enum.Values {
		serialized := member.SerializedAs
		if serialized == "" {
			serialized = member.Name
		}
		if text == serialized {
			return nil
		}
	}
	return fmt.Errorf("%s has invalid %s value %q", path, enum.Name, text)
}

func validateCompositeUnion(schema *ir.Schema, externalEnums map[string]*ir.EnumDef, union *ir.UnionDef, value any, path string) error {
	discriminatorName := ""
	discriminatorMembers := make(map[string][]*ir.TypeDef)
	hasDiscriminator := len(union.Types) > 0
	for _, memberName := range union.Types {
		member, ok := schema.Types[memberName]
		if !ok {
			hasDiscriminator = false
			break
		}
		var discriminator *ir.FieldDef
		for _, field := range member.Fields {
			if field.InternalMetadata && field.Default != nil {
				discriminator = field
				break
			}
		}
		if discriminator == nil {
			hasDiscriminator = false
			break
		}
		if discriminatorName == "" {
			discriminatorName = discriminator.Name
		} else if discriminator.Name != discriminatorName {
			hasDiscriminator = false
			break
		}
		discriminatorMembers[*discriminator.Default] = append(discriminatorMembers[*discriminator.Default], member)
	}
	if fields, ok := value.(map[string]any); hasDiscriminator && ok {
		if discriminatorValue, exists := fields[discriminatorName]; exists {
			literal := compositeLiteralString(discriminatorValue)
			selected := discriminatorMembers[literal]
			switch len(selected) {
			case 0:
				return fmt.Errorf(
					"%s.%s value %q does not select a %s union member",
					path,
					discriminatorName,
					literal,
					union.Name,
				)
			case 1:
				return validateCompositeObject(schema, externalEnums, selected[0], value, path)
			default:
				names := make([]string, 0, len(selected))
				for _, member := range selected {
					names = append(names, member.Name)
				}
				return fmt.Errorf(
					"%s.%s value %q ambiguously selects multiple %s union members: %s",
					path,
					discriminatorName,
					literal,
					union.Name,
					strings.Join(names, ", "),
				)
			}
		}
	}

	var matches []string
	for _, memberName := range union.Types {
		member, ok := schema.Types[memberName]
		if !ok {
			continue
		}
		if validateCompositeObject(schema, externalEnums, member, value, path) == nil {
			matches = append(matches, memberName)
		}
	}
	switch len(matches) {
	case 0:
		return fmt.Errorf("%s does not match any %s union member", path, union.Name)
	case 1:
		return nil
	default:
		return fmt.Errorf(
			"%s ambiguously matches multiple %s union members: %s",
			path,
			union.Name,
			strings.Join(matches, ", "),
		)
	}
}

func validateCompositeObject(schema *ir.Schema, externalEnums map[string]*ir.EnumDef, object *ir.TypeDef, value any, path string) error {
	fields, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be an object", path)
	}
	known := make(map[string]*ir.FieldDef, len(object.Fields))
	for _, field := range object.Fields {
		known[field.Name] = field
		fieldValue, exists := fields[field.Name]
		if !exists {
			if field.Required {
				return fmt.Errorf("%s.%s is required", path, field.Name)
			}
			continue
		}
		if fieldValue == nil {
			if field.Required {
				return fmt.Errorf("%s.%s cannot be null", path, field.Name)
			}
			continue
		}
		if err := validateCompositeValue(schema, externalEnums, field.TypeRef, fieldValue, path+"."+field.Name); err != nil {
			return err
		}
		if field.InternalMetadata && field.Default != nil && compositeLiteralString(fieldValue) != *field.Default {
			return fmt.Errorf("%s.%s must equal discriminator value %q", path, field.Name, *field.Default)
		}
		if err := validateCompositeFieldConstraints(field, fieldValue, path+"."+field.Name); err != nil {
			return err
		}
	}
	for name := range fields {
		if _, exists := known[name]; !exists {
			return fmt.Errorf("%s.%s is not declared by %s", path, name, object.Name)
		}
	}
	return nil
}

func compositeLiteralString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case bool:
		return strconv.FormatBool(typed)
	case nil:
		return "null"
	default:
		return ""
	}
}

func validateCompositeFieldConstraints(field *ir.FieldDef, value any, path string) error {
	if items, ok := value.([]any); ok {
		if field.ValidateListMin != nil && len(items) < *field.ValidateListMin {
			return fmt.Errorf("%s must contain at least %d items", path, *field.ValidateListMin)
		}
		if field.ValidateListMax != nil && len(items) > *field.ValidateListMax {
			return fmt.Errorf("%s must contain at most %d items", path, *field.ValidateListMax)
		}
	}
	if text, ok := value.(string); ok {
		if field.ValidateMinLength != nil && len(text) < *field.ValidateMinLength {
			return fmt.Errorf("%s must contain at least %d characters", path, *field.ValidateMinLength)
		}
		if field.ValidateMaxLength != nil && len(text) > *field.ValidateMaxLength {
			return fmt.Errorf("%s must contain at most %d characters", path, *field.ValidateMaxLength)
		}
		if field.ValidatePattern != "" {
			pattern, err := regexp.Compile(field.ValidatePattern)
			if err != nil {
				return fmt.Errorf("%s has invalid validation pattern: %w", path, err)
			}
			if !pattern.MatchString(text) {
				return fmt.Errorf("%s does not match its validation pattern", path)
			}
		}
	}
	if number, ok := value.(json.Number); ok {
		parsed, err := number.Float64()
		if err != nil {
			return fmt.Errorf("%s must be a valid number", path)
		}
		if field.ValidateMin != nil && parsed < *field.ValidateMin {
			return fmt.Errorf("%s must be at least %s", path, strconv.FormatFloat(*field.ValidateMin, 'g', -1, 64))
		}
		if field.ValidateMax != nil && parsed > *field.ValidateMax {
			return fmt.Errorf("%s must be at most %s", path, strconv.FormatFloat(*field.ValidateMax, 'g', -1, 64))
		}
	}
	return nil
}
