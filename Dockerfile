# syntax=docker/dockerfile:1.7
# Build stages run natively; only the Go compiler targets the image platform.
FROM --platform=$BUILDPLATFORM node:22-bookworm-slim AS dashboard
ENV NEXT_TELEMETRY_DISABLED=1
WORKDIR /src
RUN corepack enable && corepack prepare pnpm@10.30.3 --activate
COPY web/package.json web/pnpm-lock.yaml ./web/
RUN pnpm --dir web install --frozen-lockfile
COPY web ./web
COPY llms.txt ./llms.txt
RUN mkdir -p web/public && cp llms.txt web/public/llms.txt && pnpm --dir web build

FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm AS builder
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=dashboard /src/web/out ./web/out
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X=main.version=${VERSION}" -o /pocket-ai-gateway ./cmd/pocket-ai-gateway && mkdir /data /data_backups /backups

FROM gcr.io/distroless/static-debian12:nonroot
ENV POCKET_AI_GATEWAY_LISTEN=0.0.0.0:8080 \
    POCKET_AI_GATEWAY_DATA_DIR=/data \
    POCKET_AI_GATEWAY_PUBLIC_URL=http://localhost:8080
COPY --from=builder --chown=nonroot:nonroot /pocket-ai-gateway /usr/local/bin/pocket-ai-gateway
COPY --from=builder /src/LICENSE /src/THIRD_PARTY_NOTICES.md /usr/share/licenses/pocket-ai-gateway/
COPY --from=builder --chown=nonroot:nonroot /data /data
COPY --from=builder --chown=nonroot:nonroot /data_backups /data_backups
COPY --from=builder --chown=nonroot:nonroot /backups /backups
VOLUME ["/data", "/data_backups", "/backups"]
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/pocket-ai-gateway"]
CMD ["serve", "--allow-insecure-http"]
