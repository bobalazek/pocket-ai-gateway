# Contributing

Thanks for helping improve Pocket AI Gateway.

## Development

Install Go 1.27.1, Node.js 22 or newer, and pnpm 10.30.3. Then run:

```sh
pnpm --dir web install --frozen-lockfile
./scripts/verify.sh
```

Keep protocol namespaces separate, route frontend requests through `web/src/lib/api-client.ts`, and add a focused test for security, accounting, translation, migration, or recovery logic. Do not add provider compatibility claims without deterministic fixtures or recorded live evidence.

## Go conventions

- Let `gofmt` define formatting. The verification script also runs `go vet`, Staticcheck, tests, race checks, and cross-builds.
- Keep packages feature-focused and place types near the code that owns them. Split a file when it contains separate responsibilities or becomes difficult to review; there is no fixed line limit.
- Prefer the standard library and concrete types. Add an interface at the consuming boundary only when it has a real alternate implementation or test seam.
- Share validation and security invariants that must not drift. Keep small feature-specific error mappings local instead of building a generic handler framework.
- Every goroutine needs an explicit lifetime, cancellation path, and bounded work queue.

Submit a focused pull request with the behavior change, compatibility impact, and verification performed. Changes to public behavior must update the relevant OpenAPI document and guide.

## Security

Do not open a public issue for a suspected vulnerability. Follow [SECURITY.md](SECURITY.md).
