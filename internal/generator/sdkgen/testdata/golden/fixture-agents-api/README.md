# @schemas/fixture-agents-api-sdk

TypeScript SDK for the fixture-agents-api API. Auto-generated from GraphQL schema.

**Do not edit manually.** Regenerate via: `superschematic build schemas/fixture-agents-api/schema.json` (from the schemas root).

## Installation

This is a workspace package. Add it from the repository that generated it:

```bash
bun add @schemas/fixture-agents-api-sdk
```

## Usage

### Basic Setup

```typescript
import { FixtureAgentsApiSDK } from '@schemas/fixture-agents-api-sdk';

const sdk = new FixtureAgentsApiSDK({
  baseUrl: 'https://api.example.com',
  auth: {
    token: 'your-jwt-token'
  }
});
```

### Authentication

The SDK supports multiple authentication strategies:

#### Static Token

```typescript
const sdk = new FixtureAgentsApiSDK({
  baseUrl: 'https://api.example.com',
  auth: {
    token: 'your-static-jwt-token'
  }
});
```

#### Dynamic Token (Async)

```typescript
const sdk = new FixtureAgentsApiSDK({
  baseUrl: 'https://api.example.com',
  auth: {
    getToken: async () => {
      return await getTokenFromStorage();
    }
  }
});
```

#### Token Refresh

The SDK can automatically refresh expired tokens:

```typescript
const sdk = new FixtureAgentsApiSDK({
  baseUrl: 'https://api.example.com',
  auth: {
    getToken: async () => await getTokenFromStorage(),
    refreshToken: async () => {
      // Your token refresh logic
      const response = await fetch('/auth/refresh', {
        method: 'POST',
        // ... refresh logic
      });
      const { token } = await response.json();
      return token;
    }
  }
});
```

#### Managing Tokens

```typescript
// Update token
sdk.setToken('new-jwt-token');

// Clear token
sdk.clearToken();
```

### Making API Calls

#### AccountNamespace

```typescript
// Example: budget.checkReserve
const result = await sdk.account.budget.checkReserve({
  // Input data
});
// budget: the operations of the Budget Account implements (D103)
//   sdk.account.budget.checkReserve
//   sdk.account.budget.observe
//   sdk.account.budget.recordUsage
//   sdk.account.budget.recordUsageFor
//   sdk.account.budget.reserve
//   sdk.account.budget.reserveFor
//   sdk.account.budget.setLimit
//   sdk.account.budget.settle
//   sdk.account.budget.settleFor
// status: the operations of Account's status, a Workflow (D103)
//   sdk.account.status.transition
```

#### JobNamespace

```typescript
// Example: lease.expireHolder
const result = await sdk.job.lease.expireHolder({
  // Input data
});
// lease: the operations of the Lease Job implements (D103)
//   sdk.job.lease.expireHolder
//   sdk.job.lease.acknowledge
//   sdk.job.lease.acquire
//   sdk.job.lease.direct
//   sdk.job.lease.expire
//   sdk.job.lease.heartbeat
//   sdk.job.lease.release
//   sdk.job.lease.resetExpiries
// retries: the operations of the Retries Job implements (D103)
//   sdk.job.retries.recordAttempt
// status: the operations of Job's status, a Workflow (D103)
//   sdk.job.status.transition
```

#### SessionNamespace

```typescript
// Example: presence.beat
const result = await sdk.session.presence.beat();
// presence: the operations of the Presence Session implements (D103)
//   sdk.session.presence.beat
//   sdk.session.presence.miss
// status: the operations of Session's status, a Workflow (D103)
//   sdk.session.status.transition
```

### Error Handling

```typescript
import {
  ApiError,
  ValidationError,
  AuthenticationError,
  AuthorizationError,
  NetworkError
} from '@schemas/fixture-agents-api-sdk';

try {
  const result = await sdk.someNamespace.someMethod(input);
} catch (error) {
  if (error instanceof ValidationError) {
    // Input validation failed
    console.error('Validation errors:', error.errors);
    console.error('All messages:', error.getAllMessages());
  } else if (error instanceof AuthenticationError) {
    // Authentication required or token expired
    console.error('Auth error:', error.message);
  } else if (error instanceof AuthorizationError) {
    // Access forbidden
    console.error('Permission denied:', error.message);
  } else if (error instanceof ApiError) {
    // Other API error
    console.error('API error:', error.statusCode, error.message);
  } else if (error instanceof NetworkError) {
    // Network/connection error
    console.error('Network error:', error.message);
  }
}
```

### Request Cancellation

```typescript
// Create abort controller
const controller = sdk.createAbortController();

// Make request with cancellation support
const promise = sdk.someNamespace.someMethod(input, controller.signal);

// Cancel the request
controller.abort();

// Handle NetworkError
try {
  const result = await promise;
} catch (error) {
  if (error instanceof NetworkError) {
    console.log('Network request failed:', error.message);
  }
}
```

### Advanced Configuration

#### Request/Response Interceptors

```typescript
const sdk = new FixtureAgentsApiSDK({
  baseUrl: 'https://api.example.com',

  // Intercept requests before sending
  requestInterceptor: async (config) => {
    console.log('Sending request:', config.url);
    // Modify config if needed
    config.headers['X-Custom-Header'] = 'value';
    return config;
  },

  // Intercept responses before returning
  responseInterceptor: async (response) => {
    console.log('Received response:', response.status);
    // Transform response if needed
    return response;
  }
});
```

#### Debug Logging

```typescript
const sdk = new FixtureAgentsApiSDK({
  baseUrl: 'https://api.example.com',
  debug: true // Enable console logging
});
```

#### Custom Timeout

```typescript
const sdk = new FixtureAgentsApiSDK({
  baseUrl: 'https://api.example.com',
  timeout: 60000 // 60 seconds (default is 30000)
});
```

## Type Safety

All inputs and outputs are fully typed using the generated types from `@schemas/fixture-agents-api-types`:

```typescript
import type { SomeInputType, SomeOutputType } from '@schemas/fixture-agents-api-types';

const input: SomeInputType = {
  // Type-safe input
};

const result: SomeOutputType = await sdk.someNamespace.someMethod(input);
```

## Validation

Input validation happens automatically before requests are sent:

```typescript
try {
  await sdk.someNamespace.someMethod({
    email: 'invalid-email' // Will fail validation
  });
} catch (error) {
  if (error instanceof ValidationError) {
    console.log(error.getFieldError('email')); // "invalid format"
  }
}
```

## Generated Files

- `index.ts` - Main SDK export
- `client.ts` - HTTP client with authentication
- `types.ts` - SDK configuration and error types
- `namespaces/` - Namespace-specific API methods

## Dependencies

- Native `fetch` - HTTP client (override via `config.fetch`)
- `@schemas/fixture-agents-api-types` - TypeScript types (peer dependency, generated from schema)
