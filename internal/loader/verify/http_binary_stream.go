package verify

import (
	ir "github.com/parable-work/superschematic/ir"
)

// checkHTTPBinaryStreams holds each @httpBinaryStream operation (D167) to
// what the routes and the SDKs carry, in every form. Its media type and
// its headers are ones an SDK can compare and a response can carry. The
// generated routes answer the JSON envelope, so the operation is a
// @manualRouteRegistration one, whose handler writes the stream. Every SDK
// method sends it a plain JSON body, so it is no webhook, which has no SDK
// method, not encrypted and no upload, and no filter writes its query.
func checkHTTPBinaryStreams(schema *ir.Schema, r *Result) {
	uploads := map[string]bool{} // types that hold a FileUpload field
	for _, types := range []map[string]*ir.TypeDef{schema.Types, schema.Inputs} {
		for _, td := range types {
			if td == nil {
				continue
			}
			for _, field := range td.Fields {
				if field != nil && field.FileUpload != nil {
					uploads[td.Name] = true
				}
			}
		}
	}
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op == nil || op.HTTPBinaryStream == nil {
				continue
			}
			owner := set.Name + "." + op.Name
			for _, err := range ir.ValidateHTTPBinaryStream(owner, op.HTTPBinaryStream) {
				r.errorf("", "%v", err)
			}
			if !op.ManualRouteRegistration {
				r.errorf("", "%s: @httpBinaryStream on an operation without @manualRouteRegistration; the generated route answers the JSON envelope, so declare it and write the stream in the route's handler", owner)
			}
			if op.Webhook {
				r.errorf("", "%s: @httpBinaryStream on a @webhook, which no SDK calls", owner)
			}
			if set.Encrypted || op.Encrypted {
				r.errorf("", "%s: @httpBinaryStream on an encrypted operation; its SDK methods send a plain JSON body", owner)
			}
			if op.Filter != nil {
				r.errorf("", "%s: @httpBinaryStream on an operation with a FilterBy; its SDK methods send no filter", owner)
			}
			for _, arg := range op.Arguments {
				if arg != nil && uploads[arg.TypeRef.Name] {
					r.errorf("", "%s: @httpBinaryStream on an operation whose input %s holds a file; its SDK methods send a JSON body, not a multipart one", owner, arg.TypeRef.Name)
				}
			}
		}
	}
}
