# Contributing

Thanks for helping improve Pocket AI Gateway.

## Development

Install Go 1.27.1, Node.js 22 or newer, and pnpm 10.30.3. Then run:

```sh
pnpm --dir web install --frozen-lockfile
./scripts/verify.sh
```

Keep protocol namespaces separate, route frontend requests through `web/src/lib/api-client.ts`, and add a focused test for security, accounting, translation, migration, or recovery logic. Do not add provider compatibility claims without deterministic fixtures or recorded live evidence.

Submit a focused pull request with the behavior change, compatibility impact, and verification performed. Changes to public behavior must update the relevant OpenAPI document and guide.

## Security

Do not open a public issue for a suspected vulnerability. Follow [SECURITY.md](SECURITY.md).
