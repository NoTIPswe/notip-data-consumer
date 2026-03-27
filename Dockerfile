# ─── Builder (CI/CD) ────────────────────────────────────────────────────────
FROM ghcr.io/notipswe/notip-go-base:v0.0.1 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o notip-app ./cmd/consumer

# ─── Production ─────────────────────────────────────────────────────────────
FROM ghcr.io/notipswe/notip-go-base:v0.0.1 AS prod

LABEL org.opencontainers.image.source="https://github.com/NoTIPswe/notip-data-consumer" \
      org.opencontainers.image.description="Go Application" \
      org.opencontainers.image.licenses="MIT"

RUN groupadd -r appuser && useradd -r -g appuser appuser \
    && apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --chown=appuser:appuser --from=builder /app/notip-app .

USER appuser

EXPOSE 9090
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD ["curl", "-fsS", "http://localhost:9090/healthz"]

CMD ["./notip-app"]
