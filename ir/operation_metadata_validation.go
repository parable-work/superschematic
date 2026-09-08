package ir

import (
	"fmt"
	"strings"
)

// ValidateOperationTransportMetadata validates operation transport metadata on schema fields.
func ValidateOperationTransportMetadata(schema *Schema) error {
	if schema == nil {
		return nil
	}

	for typeName, def := range schema.Types {
		if def == nil {
			continue
		}
		if err := validateFieldTransportMetadataList(
			def.Fields,
			fmt.Sprintf(`type "%s"`, typeName),
		); err != nil {
			return err
		}
	}

	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		if err := validateFieldTransportMetadataList(
			set.Operations,
			fmt.Sprintf(`operation set "%s"`, set.Name),
		); err != nil {
			return err
		}
	}

	return nil
}

func validateFieldTransportMetadataList(fields []*FieldDef, scope string) error {
	for _, field := range fields {
		if field == nil {
			continue
		}
		context := fmt.Sprintf("field %q on %s", field.Name, scope)
		if err := validateFieldTransportMetadata(field, context); err != nil {
			return err
		}
	}
	return nil
}

func validateFieldTransportMetadata(field *FieldDef, context string) error {
	if stream := field.HTTPBinaryStream; stream != nil {
		if strings.TrimSpace(stream.ContentType) == "" {
			return fmt.Errorf("%s has httpBinaryStream without contentType", context)
		}
		for i, header := range stream.ResponseHeaders {
			if strings.TrimSpace(header) == "" {
				return fmt.Errorf("%s has httpBinaryStream with empty responseHeaders[%d]", context, i)
			}
		}
	}

	return nil
}
