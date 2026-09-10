# ── Stage 1: build ────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS build

RUN apk add --no-cache ca-certificates

WORKDIR /app

# Trust the corporate CA bundle if present in the build context (non-empty file).
# `task build-image` places a copy or empty placeholder at dictionary/_corp-ca.pem.
COPY _corp-ca.pem /tmp/_corp-ca.pem
RUN if [ -s /tmp/_corp-ca.pem ]; then \
        cp /tmp/_corp-ca.pem /usr/local/share/ca-certificates/corp.crt && \
        update-ca-certificates; \
    fi

# Dependency cache layer: copy manifests and fetch modules before copying source.
COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./
COPY internal/ ./internal/

RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /dictionary .

# ── Stage 2: runtime ──────────────────────────────────────────────────────────
FROM scratch

COPY --from=build /dictionary /dictionary

# securityContext.runAsNonRoot: true + runAsUser: 65532 works on scratch (numeric UID).
EXPOSE 8100

ENTRYPOINT ["/dictionary"]
