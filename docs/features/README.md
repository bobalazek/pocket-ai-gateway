# Feature contracts

The [API reference](../reference/api.md) owns the complete route map and compatibility matrix. These files give each compatibility/auth feature its own boundary and example shapes.

| Feature | Contract |
| --- | --- |
| OpenAI compatibility | [openai-compatible.md](openai-compatible.md) |
| Anthropic compatibility | [anthropic-compatible.md](anthropic-compatible.md) |
| Gemini compatibility | [gemini-compatible.md](gemini-compatible.md) |
| Authentication | [auth.md](auth.md) |

Examples are illustrative and abbreviated, not complete JSON Schemas or successful live responses. Implementation adds versioned schema fixtures under each feature and validates real SDK-generated requests. Global models/providers/keys/usage and other features are specified in the data model, management API, dashboard, and phase files.
