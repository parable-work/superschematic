package ir

import (
	"fmt"
	"mime"
	"net/textproto"
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

// ValidateHTTPBinaryStream checks an @httpBinaryStream (D167) as the
// generated SDKs read it. contentType is the bare media type an SDK
// compares a success response against: lowercase type/subtype without
// parameters, and not JSON, since a JSON success body is the envelope
// every other operation answers. Each of responseHeaders is a header
// name, listed once. owner names the operation or the decorator.
func ValidateHTTPBinaryStream(owner string, stream *HTTPBinaryStreamDef) []error {
	if stream == nil {
		return nil
	}
	var errs []error
	contentType := stream.ContentType
	mediaType, params, err := mime.ParseMediaType(contentType)
	kind, subtype, slash := strings.Cut(mediaType, "/")
	if err != nil || len(params) > 0 || mediaType != contentType || !slash || kind == "" || subtype == "" {
		errs = append(errs, fmt.Errorf("%s: httpBinaryStream contentType %q is not a lowercase media type without parameters, such as \"application/vnd.apache.arrow.stream\"", owner, contentType))
	} else if mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") {
		errs = append(errs, fmt.Errorf("%s: httpBinaryStream contentType %q is JSON; a JSON result is the operation's declared type, without the decorator", owner, contentType))
	}
	seen := map[string]bool{}
	for _, header := range stream.ResponseHeaders {
		canonical := textproto.CanonicalMIMEHeaderKey(header)
		switch {
		case !isHeaderName(header):
			errs = append(errs, fmt.Errorf("%s: httpBinaryStream responseHeaders holds %q, which is not a header name, such as \"X-Export-Id\"", owner, header))
		case seen[canonical]:
			errs = append(errs, fmt.Errorf("%s: httpBinaryStream responseHeaders holds %q twice", owner, header))
		}
		seen[canonical] = true
	}
	return errs
}
