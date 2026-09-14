# syntax=docker/dockerfile:1.7
FROM node:22-bookworm-slim AS dashboard
ENV NEXT_TELEMETRY_DISABLED=1
WORKDIR /src
RUN corepack enable && corepack prepare pnpm@10.30.3 --activate
COPY web/package.json web/pnpm-lock.yaml ./web/
RUN pnpm --dir web install --frozen-lockfile
COPY web ./web
COPY llms.txt ./llms.txt
RUN mkdir -p web/public && cp llms.txt web/public/llms.txt && pnpm --dir web build

FROM golang:1.27.1-bookworm AS builder
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=dashboard /src/web/out ./web/out
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X=main.version=${VERSION}" -o /pocket-ai-gateway ./cmd/pocket-ai-gateway && mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder --chown=nonroot:nonroot /pocket-ai-gateway /usr/local/bin/pocket-ai-gateway
COPY --from=builder --chown=nonroot:nonroot /data /data
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/pocket-ai-gateway"]
CMD ["help"]
