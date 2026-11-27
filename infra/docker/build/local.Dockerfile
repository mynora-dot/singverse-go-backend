ARG NODE_IMAGE=node:22-bookworm-slim@sha256:de170b39439eb0e5a3444ad7d60b015614a381e10fecae2e30ec9e735122f38d
ARG GO_IMAGE=golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea
ARG NGINX_IMAGE=nginx:stable-alpine@sha256:47928bf67eb62a1863cb08d9ecb52d3e704304ba614eaee17884204677702951

# The frontend checkout is selected explicitly through a named build context.
FROM --platform=$BUILDPLATFORM ${NODE_IMAGE} AS frontend-build
ARG SINGVERSE_VERSION=0.0.0
WORKDIR /frontend
COPY --from=frontend-source /package.json /package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY --from=frontend-source /tsconfig.json /vite.config.ts ./
COPY --from=frontend-source /src/ ./src/
COPY --from=frontend-source /admin/ ./admin/
COPY --from=frontend-source /party/ ./party/
COPY --from=frontend-source /downloads/ ./downloads/
COPY --from=frontend-source /public/ ./public/
RUN npm run build

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS backend-build
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
ARG TARGETOS
ARG TARGETARCH
ARG SINGVERSE_VERSION=0.0.0
WORKDIR /workspace
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY --from=frontend-build /frontend/dist/ ./internal/web/dist/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X main.version=${SINGVERSE_VERSION}" -o /out/singverse ./cmd/singverse
RUN set -eu; case "$TARGETARCH" in \
    amd64) asset=linux-amd64; digest=f855e99a0f0a81322014bf686b1cb820d2b4d8074e4eadafd64b851b80ecb15a ;; \
    arm64) asset=linux-arm64; digest=42de2bf27989192b9ca8db4cfaa0a9d6b4aee154a03abef30239b7186b1baf82 ;; \
    *) exit 1 ;; esac; \
    curl --fail --location --silent --show-error "https://github.com/amacneil/dbmate/releases/download/v2.37.0/dbmate-$asset" -o /out/dbmate; \
    echo "$digest  /out/dbmate" | sha256sum -c -; chmod 755 /out/dbmate

FROM ${NGINX_IMAGE} AS singverse-local
WORKDIR /opt/singverse
RUN rm -rf /etc/nginx /usr/share/nginx /docker-entrypoint.d /usr/sbin/nginx \
    && mkdir -p /data /secrets /downloads \
    && chown 10001:10001 /data /secrets /downloads
COPY --from=backend-build /out/singverse /usr/local/bin/singverse
COPY --from=backend-build /out/dbmate /usr/local/bin/dbmate
COPY internal/platform/qishui/LICENSE.txt ./licenses/qishui-music-api.txt
COPY database/migrations/ ./database/migrations/
# Optional local packages are validated before the build and bundled in the image.
COPY --from=client-releases / /opt/singverse/releases/
ENV DBMATE_BIN=/usr/local/bin/dbmate SINGVERSE_CLIENT_RELEASE_DIRECTORY=/opt/singverse/releases
ARG SOURCE_REVISION=local
ARG SINGVERSE_VERSION=0.0.0
LABEL org.opencontainers.image.title="SingVerse Local" org.opencontainers.image.revision="${SOURCE_REVISION}" \
    org.opencontainers.image.version="${SINGVERSE_VERSION}"
USER 10001:10001
EXPOSE 8080
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=5s --timeout=3s --start-period=30s --retries=6 \
    CMD wget --quiet --spider http://127.0.0.1:8080/api/health || exit 1
ENTRYPOINT ["/usr/local/bin/singverse"]
