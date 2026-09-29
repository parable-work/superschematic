package apigen

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// operationEncrypted reports whether the client sends the operation's
// request body as one encrypted envelope, which the Go server decrypts
// before it decodes any argument: the operation is in an Encrypted set, is
// declared @encrypted or returns an EncryptedField<T> (op.Encrypted), or
// takes an EncryptedField<T> argument.
func operationEncrypted(set *ir.OperationSet, op *ir.FieldDef) bool {
	if set.Encrypted || op.Encrypted {
		return true
	}
	for _, arg := range op.Arguments {
		if arg != nil && arg.Encrypted {
			return true
		}
	}
	return false
}

// checkEncryptedArguments refuses an EncryptedField<T> argument outside the
// encrypted envelope: a path or query parameter, or any argument of an
// operation whose method is not POST, PUT or PATCH, the methods whose body
// the SDKs encrypt. The loader's verify pass refuses the same declarations;
// this also catches a path parameter an operation without a rest path takes
// from its type.
func checkEncryptedArguments(namespace string, op *ir.FieldDef, method string, pathParams, queryParams []Param) error {
	outside := map[string]string{}
	for _, param := range pathParams {
		outside[param.Name] = "a path parameter"
	}
	for _, param := range queryParams {
		outside[param.Name] = "a query parameter"
	}
	for _, arg := range op.Arguments {
		if arg == nil || !arg.Encrypted {
			continue
		}
		if what, ok := outside[arg.Name]; ok {
			return fmt.Errorf("apigen: operation %s.%s argument %s is EncryptedField<T> but is %s, which travels outside the encrypted request body", namespace, op.Name, arg.Name, what)
		}
		switch method {
		case "POST", "PUT", "PATCH":
		default:
			return fmt.Errorf("apigen: operation %s.%s argument %s is EncryptedField<T>, which only a POST, PUT or PATCH request body carries encrypted; %s is not one", namespace, op.Name, arg.Name, method)
		}
	}
	return nil
}
