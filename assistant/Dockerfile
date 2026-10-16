# syntax=docker/dockerfile:1.7
# ── Stage 1: build ────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS build

RUN apk add --no-cache ca-certificates

WORKDIR /app

# Trust the corporate CA bundle if present (see phraseforge/Dockerfile for why
# the directory is bind-mounted rather than copied).
RUN --mount=type=bind,source=.docker/corp-ca,target=/corp-ca \
    if [ -s /corp-ca/_corp-ca.pem ]; then \
        cp /corp-ca/_corp-ca.pem /usr/local/share/ca-certificates/corp.crt && \
        update-ca-certificates; \
    fi

# The shared module (its LLM client) comes in as a named build context, as for
# the other apps; go.mod points at ../shared for local builds.
COPY go.mod go.sum ./
COPY --from=shared . ./shared/
RUN go mod edit -replace k8s-lab/shared=./shared && go mod download

COPY *.go ./
COPY internal/ ./internal/
COPY schemas/ ./schemas/

RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /assistant .

# ── Stage 2: runtime ──────────────────────────────────────────────────────────
FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /assistant /assistant

# securityContext.runAsNonRoot: true + runAsUser: 65532 works on scratch (numeric UID).
EXPOSE 8080

ENTRYPOINT ["/assistant"]
