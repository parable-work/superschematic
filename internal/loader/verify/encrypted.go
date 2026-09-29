package verify

import (
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// checkEncryptedArguments refuses an EncryptedField<T> argument the
// encrypted request envelope does not carry. The envelope is the whole
// request body of a POST, PUT or PATCH operation, which the SDKs encrypt and
// the Go server decrypts, so a query parameter, a path parameter and any
// argument of a GET or DELETE operation (or of one without a method) would
// travel unencrypted. apigen refuses the one case this pass cannot see: a
// path parameter an operation without a rest path takes from its type.
func checkEncryptedArguments(schema *ir.Schema, r *Result) {
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op == nil {
				continue
			}
			pathParams := restPathParams(op.RestPath)
			method := strings.ToUpper(op.HTTPMethod)
			owner := set.Name + "." + op.Name
			for _, arg := range op.Arguments {
				if arg == nil || !arg.Encrypted {
					continue
				}
				switch {
				case arg.IsQuery || op.ParamType == "query":
					r.errorf("", "%s: query parameter %q cannot be EncryptedField<T>: the query string travels outside the encrypted request body", owner, arg.Name)
				case pathParams[arg.Name]:
					r.errorf("", "%s: path parameter %q cannot be EncryptedField<T>: the path travels outside the encrypted request body", owner, arg.Name)
				case method != "POST" && method != "PUT" && method != "PATCH":
					r.errorf("", "%s: argument %q is EncryptedField<T>, which only a POST, PUT or PATCH request body carries encrypted; declare one of those methods", owner, arg.Name)
				}
			}
		}
	}
}
