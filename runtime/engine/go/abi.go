package engine

/*
#cgo CFLAGS: -I${SRCDIR}/include
#include "superschematic_engine.h"
*/
import "C"

import "unsafe"

// code is what an export returns.
type code int32

const (
	codeOK           = code(C.ENG_OK)
	codeError        = code(C.ENG_ERROR)
	codeBadArguments = code(C.ENG_BAD_ARGUMENTS)
	codeClosed       = code(C.ENG_CLOSED)
)

// ABIVersion is the version of the C ABI this binding speaks
// (ENG_ABI_VERSION): Open refuses an archive of another.
const ABIVersion = uint32(C.ENG_ABI_VERSION)

// in is a byte slice as an input pointer: nil for none. cgo lets C read a
// Go slice that holds no Go pointers for the length of the call.
func in(bytes []byte) *C.uint8_t {
	if len(bytes) == 0 {
		return nil
	}
	return (*C.uint8_t)(unsafe.Pointer(&bytes[0]))
}

// taken copies an output into Go memory and releases it.
func taken(ptr *C.uint8_t, length C.size_t) []byte {
	if ptr == nil {
		return nil
	}
	defer C.eng_free(ptr, length)
	return C.GoBytes(unsafe.Pointer(ptr), C.int(length))
}

func abiVersion() uint32 {
	return uint32(C.eng_abi_version())
}

func engOpen(config []byte) (uint64, []byte, code) {
	var handle C.eng_engine
	var out *C.uint8_t
	var outLen C.size_t
	c := code(C.eng_open(in(config), C.size_t(len(config)), &handle, &out, &outLen))
	return uint64(handle), taken(out, outLen), c
}

func engClose(handle uint64) code {
	return code(C.eng_close(C.eng_engine(handle)))
}

func engCall(handle uint64, request []byte) ([]byte, code) {
	var out *C.uint8_t
	var outLen C.size_t
	c := code(C.eng_call(C.eng_engine(handle), in(request), C.size_t(len(request)), &out, &outLen))
	return taken(out, outLen), c
}

func engHTTP(handle uint64, head, body []byte) ([]byte, []byte, code) {
	var headOut, bodyOut *C.uint8_t
	var headLen, bodyLen C.size_t
	c := code(C.eng_http(C.eng_engine(handle),
		in(head), C.size_t(len(head)),
		in(body), C.size_t(len(body)),
		&headOut, &headLen, &bodyOut, &bodyLen))
	return taken(headOut, headLen), taken(bodyOut, bodyLen), c
}

func engStreamPoll(handle, stream uint64) ([]byte, code) {
	var out *C.uint8_t
	var outLen C.size_t
	c := code(C.eng_stream_poll(C.eng_engine(handle), C.eng_stream(stream), &out, &outLen))
	return taken(out, outLen), c
}

func engStreamClose(handle, stream uint64) code {
	return code(C.eng_stream_close(C.eng_engine(handle), C.eng_stream(stream)))
}

func engWait(handle, after uint64, timeoutMs uint32) (uint64, code) {
	var epoch C.uint64_t
	c := code(C.eng_wait(C.eng_engine(handle), C.uint64_t(after), C.uint32_t(timeoutMs), &epoch))
	return uint64(epoch), c
}

func engRunner(handle uint64, request []byte) ([]byte, code) {
	var out *C.uint8_t
	var outLen C.size_t
	c := code(C.eng_runner(C.eng_engine(handle), in(request), C.size_t(len(request)), &out, &outLen))
	return taken(out, outLen), c
}
