# The dependency cache image (see Dockerfile.cache / the docker:cache stage in
# .cnb.yml) already contains the gcc/musl-dev toolchain and the downloaded Go
# modules. Using it as the builder base means those two expensive steps are
# never repeated: this Dockerfile only compiles the binary.
#
# CACHE_IMAGE defaults to the same base image the cache is built from, so a
# standalone `docker build .` still works without the pipeline around it.
# The alpine minor version is pinned so the default matches the runtime stage
# below (see also the FROM line in Dockerfile.cache).
ARG CACHE_IMAGE=golang:1.26-alpine3.23
FROM ${CACHE_IMAGE} AS builder

WORKDIR /app

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
ARG CPA_TOKEN=""

# -buildvcs=false keeps the output independent of the git metadata that differs
# between local and CI checkouts.
RUN CGO_ENABLED=1 GOOS=linux go build -buildvcs=false -ldflags="-s -w -X 'main.Version=${VERSION}-plus' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}' -X 'main.CPAToken=${CPA_TOKEN}'" -o ./CLIProxyAPIPlus ./cmd/server/

# Keep this branch in sync with the base image of the cache/builder stages
# (Dockerfile.cache, CACHE_IMAGE above): the CGO binary is linked against the
# builder's musl-dev, so the runtime must carry the same musl.
FROM alpine:3.23

# Keep in sync with ALPINE_BRANCH in Dockerfile.cache.
ARG ALPINE_BRANCH=v3.23
ARG APK_MIRROR=https://mirrors.aliyun.com/alpine

# libc6-compat is required by the CGO-linked binary (go-sqlite3).
#
# The Alpine branch and mirror are pinned explicitly instead of rewriting
# whatever repositories the base image ships: otherwise the same apk command
# can resolve to a different package set between builds, which is how a single
# package (musl-dev) once turned into a 2-5 minute stall.
RUN set -eux; \
    printf '%s/%s/main\n%s/%s/community\n' \
        "${APK_MIRROR}" "${ALPINE_BRANCH}" \
        "${APK_MIRROR}" "${ALPINE_BRANCH}" \
        > /etc/apk/repositories; \
    rm -rf /var/cache/apk/*; \
    for i in 1 2 3; do \
        if apk add --no-cache tzdata libc6-compat; then break; fi; \
        if [ "$i" = "3" ]; then exit 1; fi; \
        echo "apk add failed (attempt $i), retrying in 5s..."; \
        sleep 5; \
    done; \
    rm -rf /var/cache/apk/*

RUN mkdir /CLIProxyAPI

COPY --from=builder /app/CLIProxyAPIPlus /CLIProxyAPI/CLIProxyAPIPlus

COPY config.example.yaml /CLIProxyAPI/config.example.yaml

# The hosts pin must happen at container start: Docker replaces the image's
# /etc/hosts with its own managed file at runtime, so a Dockerfile RUN write
# would never survive. An entrypoint write does.
COPY entrypoint.sh /CLIProxyAPI/entrypoint.sh

WORKDIR /CLIProxyAPI

EXPOSE 8317

ENV TZ=Asia/Shanghai

RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

ENTRYPOINT ["sh", "/CLIProxyAPI/entrypoint.sh"]

CMD ["./CLIProxyAPIPlus"]
