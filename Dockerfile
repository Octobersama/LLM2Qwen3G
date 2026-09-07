# syntax=docker/dockerfile:1
# LLM2Qwen3Guard gateway — multi-stage build.
# Stage 1 builds a static binary (CGO disabled; the gateway is stdlib-only).
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod ./
# The module has zero third-party dependencies, so there is no go.sum and no
# layer-cached `go mod download` step.
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false \
    -ldflags "-s -w" -o /out/llm2qwen3guard ./cmd/gateway

# Stage 2 runs the binary on a distroless-style minimal image.
# distroless/static-debian12 provides CA certificates (required for HTTPS
# upstreams like open.bigmodel.cn) plus tls-only runtime; nonroot user built in.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/llm2qwen3guard /usr/local/bin/llm2qwen3guard
# Run as the image's built-in nonroot uid (65532:65532).
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/llm2qwen3guard"]
