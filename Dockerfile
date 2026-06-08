FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./

ENV GOPROXY=https://goproxy.cn,direct
ENV GOSUMDB=sum.golang.google.cn
RUN go mod download

RUN apk add --no-cache gcc musl-dev

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
ARG CNB_TOKEN

RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w -X 'main.Version=${VERSION}-plus' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" -o ./CLIProxyAPIPlus ./cmd/server/

FROM alpine:3.23

RUN apk add --no-cache tzdata libc6-compat

RUN mkdir /CLIProxyAPI

COPY --from=builder /app/CLIProxyAPIPlus /CLIProxyAPI/CLIProxyAPIPlus

COPY config.example.yaml /CLIProxyAPI/config.example.yaml

WORKDIR /CLIProxyAPI

EXPOSE 8317

ENV TZ=Asia/Shanghai

RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

CMD ["./CLIProxyAPIPlus"]