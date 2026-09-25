# Stage 1 — build the web SPA.
# Doing this inside the docker build is what makes a single `docker build`
# produce a complete image. Previously the Dockerfile only ran `go build`
# and silently embedded whatever happened to be in web/dist on the host,
# so a dev who skipped `npm run build` would ship a stale UI.
FROM node:24-alpine AS web-build
WORKDIR /web

# Cache npm install separately from source so source-only edits don't bust
# the dep layer.
COPY web/package.json web/package-lock.json* ./
RUN npm ci --no-audit --no-fund

COPY web/ ./
RUN npm run build

# Stage 2 — build the Go binary, embedding the SPA from stage 1.
FROM golang:1.25-alpine AS go-build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the repo, then overwrite web/dist with the freshly
# built artifacts from stage 1 so embed.FS picks them up.
COPY . .
COPY --from=web-build /web/dist ./web/dist

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /out/gatemux \
    ./cmd/gatemux

# Optional synthetic upstream for isolated smoke tests. Never uses provider keys.
FROM go-build AS mock-build
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/mock-openai ./cmd/mock-openai

FROM gcr.io/distroless/static-debian12:nonroot AS mock
COPY --from=mock-build /out/mock-openai /mock-openai
USER nonroot:nonroot
ENTRYPOINT ["/mock-openai"]

# Default final target — distroless gateway runtime.
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=go-build /out/gatemux /gatemux
EXPOSE 4000
USER nonroot:nonroot
ENTRYPOINT ["/gatemux"]
CMD ["serve"]
