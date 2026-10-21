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

# Trust the corporate CA bundle if present — absent entirely on machines with
# no corporate TLS-inspecting proxy (CI, other developers' machines).
# .docker/corp-ca/ is bind-mounted only for this RUN step, never COPY'd into
# an image layer, so nothing needs to create a placeholder file when there's
# no bundle to trust — that's the point of mounting a (git-tracked, normally
# empty) directory here rather than the file itself: a plain COPY, or a bind
# mount of a specific file, requires that file to exist. Scoped to this one
# small directory (not the whole build context) so editing application code
# doesn't invalidate this layer's cache — see Taskfile.yml's _build-image,
# the only thing that ever places a real _corp-ca.pem here.
RUN --mount=type=bind,source=.docker/corp-ca,target=/corp-ca \
    if [ -s /corp-ca/_corp-ca.pem ]; then \
        cp /corp-ca/_corp-ca.pem /usr/local/share/ca-certificates/corp.crt && \
        update-ca-certificates; \
    fi

# Dependency cache layer: copy manifests and the local shared module before
# downloading dependencies. Dependencies are resolved by Go, not vendored into
# this repository.
COPY go.mod go.sum ./
COPY --from=shared . ./shared/
RUN go mod edit -replace k8s-lab/shared=./shared && go mod download

# The built shared/ui bundle that shared/ui/ui.go embeds (dist/ is not in git).
COPY --from=ui /ui/dist/ ./shared/ui/dist/

COPY main.go ./
COPY internal/ ./internal/

RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /phraseforge .

# ── Stage 3: runtime ──────────────────────────────────────────────────────────
FROM scratch

# scratch has no system CA bundle. Copy the bundle assembled in the build
# stage, including the corporate CA when .docker/corp-ca/_corp-ca.pem was
# supplied, so outbound HTTPS (for example OpenRouter) can be verified.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /phraseforge /phraseforge

# securityContext.runAsNonRoot: true + runAsUser: 65532 works on scratch (numeric UID).
EXPOSE 8090

ENTRYPOINT ["/phraseforge"]
