package apigen

import (
	"fmt"
	"strings"

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

// EncryptedBodyMethod reports whether method is POST, PUT or PATCH, the
// methods whose request body the SDKs send as an encrypted envelope and the
// Go router decrypts. Every other method carries no encrypted body:
// checkEncryptedMethod and checkEncryptedArguments refuse an encrypted
// operation or EncryptedField<T> argument of one. It is narrower than
// EndpointInfo.ArgumentsInBody: a DELETE reads its arguments from a body
// that is never encrypted.
func EncryptedBodyMethod(method string) bool {
	switch strings.ToUpper(method) {
	case "POST", "PUT", "PATCH":
		return true
	default:
		return false
	}
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
		if !EncryptedBodyMethod(method) {
			return fmt.Errorf("apigen: operation %s.%s argument %s is EncryptedField<T>, which only a POST, PUT or PATCH request body carries encrypted; %s is not one", namespace, op.Name, arg.Name, method)
		}
	}
	return nil
}

// checkEncryptedMethod refuses an operation its set (Encrypted), @encrypted
// or an EncryptedField<T> result makes encrypted when its method, declared
// or its set's default, is not POST, PUT or PATCH. Such a request has no
// body: the SDKs send it unencrypted, and the router's payload decryptor
// would find no envelope to open. The loader's verify pass refuses a
// declared GET or DELETE; this also catches an operation without a method
// that its set's name makes a GET. checkEncryptedArguments covers an
// operation encrypted by an argument.
func checkEncryptedMethod(namespace string, set *ir.OperationSet, op *ir.FieldDef, method string) error {
	if !set.Encrypted && !op.Encrypted {
		return nil
	}
	if EncryptedBodyMethod(method) {
		return nil
	}
	return fmt.Errorf("apigen: operation %s.%s is encrypted (an Encrypted operation set, @encrypted, or an EncryptedField<T> result), but a %s request has no body to encrypt; declare POST, PUT or PATCH, or drop the encryption", namespace, op.Name, method)
}
