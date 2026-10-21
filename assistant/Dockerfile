# syntax=docker/dockerfile:1.7
# ── Stage 1: shared UI bundle ─────────────────────────────────────────────────
# shared/ui (TypeScript, Lit) is bundled here and embedded by the Go build
# below. Only the files the bundle needs are copied, so editing the Go code
# or the other shared packages does not invalidate this stage's cache.
FROM node:26-alpine AS ui

WORKDIR /ui

COPY --from=shared ui/package.json ui/package-lock.json ./
# Same corporate-CA handling as the build stage: node ignores the system store,
# so the bundle is handed to node (and so to npm) through NODE_EXTRA_CA_CERTS.
RUN --mount=type=bind,source=.docker/corp-ca,target=/corp-ca \
    if [ -s /corp-ca/_corp-ca.pem ]; then export NODE_EXTRA_CA_CERTS=/corp-ca/_corp-ca.pem; fi && \
    npm ci

COPY --from=shared ui/build.mjs ui/tsconfig.json ./
COPY --from=shared ui/src ./src
RUN npm run build

# ── Stage 2: build ────────────────────────────────────────────────────────────
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

# The built shared/ui bundle that shared/ui/ui.go embeds (dist/ is not in git).
COPY --from=ui /ui/dist/ ./shared/ui/dist/

COPY *.go ./
COPY internal/ ./internal/
COPY schemas/ ./schemas/

RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /assistant .

# ── Stage 3: runtime ──────────────────────────────────────────────────────────
FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /assistant /assistant

# securityContext.runAsNonRoot: true + runAsUser: 65532 works on scratch (numeric UID).
EXPOSE 8080

ENTRYPOINT ["/assistant"]
