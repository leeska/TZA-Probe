FROM golang:1.25-alpine@sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /tza-server ./cmd/server

FROM alpine:3.23@sha256:075c2c1a4068c1e251228ca2b0ea8b163f835ca5d66e758b93c7fc89b1995616
RUN apk add --no-cache ca-certificates tzdata && addgroup -g 65532 tza && adduser -D -H -u 65532 -G tza tza && mkdir /data && chown tza:tza /data
COPY --from=build /tza-server /usr/local/bin/tza-server
USER 65532:65532
VOLUME /data
EXPOSE 25774
ENTRYPOINT ["/usr/local/bin/tza-server"]
CMD ["--listen", "0.0.0.0:25774", "--data", "/data"]
