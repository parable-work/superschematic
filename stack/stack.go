// Package stack is the public face of the stack model's resolver
// (docs/stack-model.md, section 5) and of its deploy (sections 7.3 and
// 11): an extension's tests resolve a stack over its platforms and deploy
// it through its seams with it, and a command writes the result. Every
// identifier is an alias or a one-line forward over internal/stack or
// internal/stackdeploy, as the registry and loader packages are (D2).
package stack

import (
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
)

type (
	// Input is a stack, the facts of the services it references and the
	// environment to resolve.
	Input = stack.Input
	// Service, Config, ConfigField and Operation are the facts of one
	// service.
	Service     = stack.Service
	Config      = stack.Config
	ConfigField = stack.ConfigField
	Operation   = stack.Operation
	// Errors are every failure one resolution found; Error is one, and
	// Code names its check.
	Errors = stack.Errors
	Error  = stack.Error
	Code   = stack.Code
)

// The codes of the checks of docs/stack-model.md, section 5.2, and of the
// other failures; see internal/stack.
const (
	CodeUnboundField      = stack.CodeUnboundField
	CodeUnknownEnvKey     = stack.CodeUnknownEnvKey
	CodeSecretLiteral     = stack.CodeSecretLiteral
	CodeKindMismatch      = stack.CodeKindMismatch
	CodeUnrealizable      = stack.CodeUnrealizable
	CodeNoConnector       = stack.CodeNoConnector
	CodeExposeNotServer   = stack.CodeExposeNotServer
	CodePolicy            = stack.CodePolicy
	CodeUnreachableEdge   = stack.CodeUnreachableEdge
	CodeInvalidStack      = stack.CodeInvalidStack
	CodeUnknownService    = stack.CodeUnknownService
	CodeUnknownDeployable = stack.CodeUnknownDeployable
	CodeUnknownTarget     = stack.CodeUnknownTarget
	CodeUnknownPlatform   = stack.CodeUnknownPlatform
	CodeInvalidValues     = stack.CodeInvalidValues
	CodeInvalidSettings   = stack.CodeInvalidSettings
	CodeUnknownParameter  = stack.CodeUnknownParameter
	CodeFieldCollision    = stack.CodeFieldCollision
	CodeAmbiguousDatabase = stack.CodeAmbiguousDatabase
	CodeCallCycle         = stack.CodeCallCycle
	CodeLowering          = stack.CodeLowering
	CodeGraph             = stack.CodeGraph
)

// OperationsOf reads an API service's operations for Service.Operations;
// see internal/stack.OperationsOf.
func OperationsOf(schema *ir.Schema) []Operation { return stack.OperationsOf(schema) }

// EnvironmentFile is the name of the resolved environment's file.
const EnvironmentFile = stack.EnvironmentFile

// Resolve resolves one environment of a stack; see internal/stack.Resolve.
func Resolve(reg *registry.Registry, in Input) (*ir.ResolvedEnvironment, error) {
	return stack.Resolve(reg, in)
}

// Servers returns the servers of a stack, which no environment changes;
// see internal/stack.Servers.
func Servers(in Input) ([]*ir.ResolvedDeployable, error) { return stack.Servers(in) }

// DerivedField returns the config field an edge fills; see
// internal/stack.DerivedField.
func DerivedField(kind ir.EdgeKind, service string) string { return stack.DerivedField(kind, service) }

// EnvironmentPath returns where a resolved environment is written; see
// internal/stack.EnvironmentPath.
func EnvironmentPath(outputRoot, stackName, environment string) string {
	return stack.EnvironmentPath(outputRoot, stackName, environment)
}

// Marshal encodes a resolved environment in its stable JSON form; see
// internal/stack.Marshal.
func Marshal(env *ir.ResolvedEnvironment) ([]byte, error) { return stack.Marshal(env) }

// Unmarshal decodes an environment.json; see internal/stack.Unmarshal.
func Unmarshal(data []byte) (*ir.ResolvedEnvironment, error) { return stack.Unmarshal(data) }

// Write writes a resolved environment under outputRoot; see
// internal/stack.Write.
func Write(outputRoot string, env *ir.ResolvedEnvironment) (string, error) {
	return stack.Write(outputRoot, env)
}
