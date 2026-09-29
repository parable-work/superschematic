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

// checkEncryptedOperations refuses a GET or DELETE operation that its set
// (Encrypted), @encrypted or an EncryptedField<T> result makes encrypted.
// Such a request has no body: the SDKs send it unencrypted, and the Go
// server's payload decryptor would find no envelope to open and answer 400.
// An operation without a method takes its set's default, which only
// generation knows; apigen refuses the same when that default is GET.
// checkEncryptedArguments covers an operation encrypted by an argument.
func checkEncryptedOperations(schema *ir.Schema, r *Result) {
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op == nil || (!set.Encrypted && !op.Encrypted) {
				continue
			}
			method := strings.ToUpper(op.HTTPMethod)
			if method != "GET" && method != "DELETE" {
				continue
			}
			why := "the operation is @encrypted or returns an EncryptedField<T>"
			if set.Encrypted {
				why = "its operation set is Encrypted"
			}
			r.errorf("", "%s.%s: %s, but a %s request has no body to encrypt; declare POST, PUT or PATCH, or drop the encryption", set.Name, op.Name, why, method)
		}
	}
}
