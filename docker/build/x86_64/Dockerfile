# syntax=docker/dockerfile:1.7
# This dockerfile should be built with `make docker-build` from the stash root.

# Build Frontend
FROM node:24-alpine AS frontend
RUN apk add --no-cache make git
WORKDIR /stash
RUN npm install -g pnpm

## cache UI dependencies separately from source changes
COPY ./ui/v2.5/package.json ./ui/v2.5/pnpm-lock.yaml ./ui/v2.5/pnpm-workspace.yaml /stash/ui/v2.5/
RUN --mount=type=cache,target=/root/.pnpm-store \
    cd /stash/ui/v2.5 && pnpm config set store-dir /root/.pnpm-store && pnpm install --frozen-lockfile

COPY Makefile /stash/
COPY ./graphql /stash/graphql/
COPY ./ui /stash/ui/
RUN make generate-ui
ARG GITHASH
ARG STASH_VERSION
RUN BUILD_DATE=$(date +"%Y-%m-%d %H:%M:%S") make ui-only

# Build Backend
FROM golang:1.24.3-alpine AS backend
RUN apk add --no-cache make alpine-sdk
WORKDIR /stash

COPY ./go.mod ./go.sum /stash/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY ./*.go Makefile gqlgen.yml .gqlgenc.yml /stash/
COPY ./graphql /stash/graphql/
COPY ./scripts /stash/scripts/
COPY ./pkg /stash/pkg/
COPY ./cmd /stash/cmd/
COPY ./internal /stash/internal/
# needed for generate-login-locale
COPY ./ui /stash/ui/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    make generate-backend generate-login-locale
COPY --from=frontend /stash /stash/
ARG GITHASH
ARG STASH_VERSION
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    make flags-release flags-pie stash

# Final Runnable Image
FROM alpine:latest
RUN apk add --no-cache ca-certificates vips-tools ffmpeg
COPY --from=backend /stash/stash /usr/bin/
ENV STASH_CONFIG_FILE=/root/.stash/config.yml
EXPOSE 9999
ENTRYPOINT ["stash"]
