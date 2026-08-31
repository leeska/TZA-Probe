FROM alpine:3.21

WORKDIR /app

# Docker buildx 会在构建时自动填充这些变量
ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache ca-certificates curl tzdata

COPY --chmod=755 tza-probe-${TARGETOS}-${TARGETARCH} /app/tza-probe

ENV GIN_MODE=release
ENV TZA_PROBE_LISTEN=0.0.0.0:25774

EXPOSE 25774

CMD ["/app/tza-probe", "server"]
