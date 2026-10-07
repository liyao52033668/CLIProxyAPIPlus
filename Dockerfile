FROM golang:1.26-alpine AS builder

WORKDIR /app

# gcc/musl-dev are required for CGO (go-sqlite3). Install them before the
# module layer so it stays cached even when dependencies change.
#
# A pinned China mirror is used because the default dl-cdn.alpinelinux.org CDN
# is intermittently unreachable from CNB runners: the gcc toolchain (~200 MB
# installed) then takes 3-6 minutes to download instead of ~13s, which is the
# main cause of the "prepare build args" stage randomly turning slow.
RUN sed -i 's#https\?://dl-cdn.alpinelinux.org#https://mirrors.aliyun.com#g' \
        /etc/apk/repositories \
    && apk add --no-cache gcc musl-dev \
    && rm -rf /var/cache/apk/*

COPY go.mod go.sum ./

ENV GOPROXY=https://goproxy.cn,direct
ENV GOSUMDB=sum.golang.google.cn

# Retry the module download: the proxy occasionally drops a connection and a
# single failure currently aborts the whole image build. Keep the failure fatal
# once the retries are exhausted, otherwise a broken module cache silently
# reaches the build step.
RUN set -eux; \
    for i in 1 2 3; do \
        if go mod download; then break; fi; \
        if [ "$i" = "3" ]; then exit 1; fi; \
        sleep 5; \
    done; \
    go mod verify

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
ARG CPA_TOKEN=""

# -buildvcs=false keeps the output independent of the git metadata that differs
# between local and CI checkouts.
RUN CGO_ENABLED=1 GOOS=linux go build -buildvcs=false -ldflags="-s -w -X 'main.Version=${VERSION}-plus' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}' -X 'main.CPAToken=${CPA_TOKEN}'" -o ./CLIProxyAPIPlus ./cmd/server/

FROM alpine:3.23

# libc6-compat is required by the CGO-linked binary (go-sqlite3).
RUN sed -i 's#https\?://dl-cdn.alpinelinux.org#https://mirrors.aliyun.com#g' \
        /etc/apk/repositories \
    && apk add --no-cache tzdata libc6-compat \
    && rm -rf /var/cache/apk/*

RUN mkdir /CLIProxyAPI

COPY --from=builder /app/CLIProxyAPIPlus /CLIProxyAPI/CLIProxyAPIPlus

COPY config.example.yaml /CLIProxyAPI/config.example.yaml

WORKDIR /CLIProxyAPI

EXPOSE 8317

ENV TZ=Asia/Shanghai

RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

CMD ["./CLIProxyAPIPlus"]
