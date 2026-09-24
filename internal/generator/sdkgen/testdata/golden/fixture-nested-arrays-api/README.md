# @schemas/fixture-nested-arrays-api-sdk

TypeScript SDK for the fixture-nested-arrays-api API. Auto-generated from GraphQL schema.

**Do not edit manually.** Regenerate via: `superschematic build schemas/fixture-nested-arrays-api/schema.json` (from the schemas root).

## Installation

This is a workspace package. Add it from the repository that generated it:

```bash
bun add @schemas/fixture-nested-arrays-api-sdk
```

## Usage

### Basic Setup

```typescript
import { FixtureNestedArraysApiSDK } from '@schemas/fixture-nested-arrays-api-sdk';

const sdk = new FixtureNestedArraysApiSDK({
  baseUrl: 'https://api.example.com',
});
```

### Making API Calls

#### GridNamespace

```typescript
// Example: saveGrid
const result = await sdk.grid.saveGrid({
  // Input data
});
```

### Error Handling

```typescript
import {
  ApiError,
  ValidationError,
  AuthenticationError,
  AuthorizationError,
  NetworkError
} from '@schemas/fixture-nested-arrays-api-sdk';

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
const sdk = new FixtureNestedArraysApiSDK({
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
const sdk = new FixtureNestedArraysApiSDK({
  baseUrl: 'https://api.example.com',
  debug: true // Enable console logging
});
```

#### Custom Timeout

```typescript
const sdk = new FixtureNestedArraysApiSDK({
  baseUrl: 'https://api.example.com',
  timeout: 60000 // 60 seconds (default is 30000)
});
```

## Type Safety

All inputs and outputs are fully typed using the generated types from `@schemas/fixture-nested-arrays-api-types`:

```typescript
import type { SomeInputType, SomeOutputType } from '@schemas/fixture-nested-arrays-api-types';

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
- `@schemas/fixture-nested-arrays-api-types` - TypeScript types (peer dependency, generated from schema)
