# syntax=docker/dockerfile:1.7
# ── Stage 1: build ────────────────────────────────────────────────────────────
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
