ARG BIN_NAME=raindrop-public-browser
ARG BIN_VERSION=<unknown>

FROM golang:1-alpine AS builder
ARG BIN_NAME
ARG BIN_VERSION

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-X main.version=${BIN_VERSION}" -o ./out/${BIN_NAME} .

FROM scratch
ARG BIN_NAME
ARG BIN_VERSION

# Allow connecting to HTTPS hosts (the Raindrop API, cover image hosts).
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /src/out/${BIN_NAME} /usr/bin/${BIN_NAME}

# Signals to the program that it is running in this image, so the login
# OAuth callback server binds to all interfaces (0.0.0.0) and a published
# port (docker run -p 8080:8080) can reach it.
ENV RAINDROP_PUBLIC_BROWSER_IN_DOCKER=1

# Users are expected to mount: the OAuth state file, the DB directory, the
# template directory (read-only), and the images directory.
USER 1000:1000

HEALTHCHECK --interval=30s --timeout=10s --start-period=10s \
	CMD ["/usr/bin/raindrop-public-browser", "healthcheck"]

ENTRYPOINT ["/usr/bin/raindrop-public-browser"]
CMD ["serve"]

LABEL license="MIT"
LABEL org.opencontainers.image.licenses="MIT"
LABEL maintainer="Chris Dzombak <https://www.dzombak.com>"
LABEL org.opencontainers.image.authors="Chris Dzombak <https://www.dzombak.com>"
LABEL org.opencontainers.image.url="https://github.com/cdzombak/raindrop-public-browser"
LABEL org.opencontainers.image.documentation="https://github.com/cdzombak/raindrop-public-browser"
LABEL org.opencontainers.image.source="https://github.com/cdzombak/raindrop-public-browser"
LABEL org.opencontainers.image.version="${BIN_VERSION}"
LABEL org.opencontainers.image.title="${BIN_NAME}"
LABEL org.opencontainers.image.description="Paginated, searchable web browser for public Raindrop.io bookmarks"
