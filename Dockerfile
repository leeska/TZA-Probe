FROM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=1.0.0-dev
# Embed Agents for both architectures so the image can serve the one-line installer.
RUN for arch in amd64 arm64; do \
      CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags="-s -w -X github.com/leeska/TZA-Probe/internal/model.Version=$VERSION" -o internal/agentdist/bin/tza-agent-linux-$arch ./cmd/agent || exit 1; \
    done \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/leeska/TZA-Probe/internal/model.Version=$VERSION" -o /tza-server ./cmd/server

FROM alpine:3.23@sha256:075c2c1a4068c1e251228ca2b0ea8b163f835ca5d66e758b93c7fc89b1995616
RUN apk add --no-cache ca-certificates tzdata && addgroup -g 65532 tza && adduser -D -H -u 65532 -G tza tza && mkdir /data && chown tza:tza /data
COPY --from=build /tza-server /usr/local/bin/tza-server
USER 65532:65532
VOLUME /data
EXPOSE 25774
ENTRYPOINT ["/usr/local/bin/tza-server"]
CMD ["--listen", "0.0.0.0:25774", "--data", "/data"]
