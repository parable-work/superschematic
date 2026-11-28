/*
@superschematic/http-runtime/providers: the runtime extension points
(D159). Five interfaces a running app calls, each implemented by a provider
package: a credential provider, a channel adapter, a sandbox provider, a
model provider and a file processor. A package exports a definition
(defineProvider) and declares the same in its package.json; a server reads
the providers its environment configures with providersFrom.
*/

export { SECRET_MASK, SecretValue, type Revealable } from '../secret.js';
export { CALLER_KEY, contractOf, extensionSubject, providerConfigField, RUNTIME_CONTRACTS, type RuntimeContract, type RuntimeInterface } from './contracts.js';
export {
  applyInjection,
  Injection,
  InjectionError,
  useCredential,
  type CredentialListing,
  type CredentialProvider,
  type CredentialQuery,
  type CredentialUse,
  type InjectionRunner,
  type InjectionTarget,
  type UseCredential,
} from './credentials.js';
export type {
  ChannelAdapter,
  ChannelHealth,
  DeliveryState,
  DeliveryStatus,
  InboundAttachment,
  InboundAuthentication,
  InboundMessage,
  OutboundMessage,
  SendResult,
  WebhookRequest,
} from './channel.js';
export type { SandboxCost, SandboxInstance, SandboxProvider, SandboxSpec, SandboxState, SandboxStatus } from './sandbox.js';
export { runAgentInstance, type AgentInstanceOptions, type AgentInstanceResult, type SandboxToolContext, type SandboxToolHandler } from './agent-runner.js';
export type { ModelCompletion, ModelEmbedding, ModelEmbedRequest, ModelMessage, ModelProvider, ModelTool, ModelToolCall, ModelTurn, ModelUsage } from './model.js';
export { readsMediaType, type ExtractedText, type FileInput, type FileProcessor, type Preview, type PreviewOptions } from './files.js';
export {
  checkProviderInstance,
  checkProviderPackage,
  defineProvider,
  extensionAuthenticator,
  extensionPrincipal,
  ProviderContractError,
  ProviderSettingError,
  ProviderSettings,
  type ExtensionKey,
  type ProviderContext,
  type ProviderDefinition,
  type ProviderInterfaces,
  type ProviderSettingSpec,
} from './define.js';
export {
  extensionKeysOf,
  ProviderConfigError,
  ProviderNotConfiguredError,
  providersFrom,
  type ConfiguredProvider,
  type Providers,
  type ProvidersOptions,
} from './registry.js';
