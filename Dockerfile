# WhatsApp bridge – multi-stage build (CGO is needed for SQLite)
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY whatsapp-bridge/go.mod whatsapp-bridge/go.sum ./
RUN go mod download
COPY whatsapp-bridge/ ./
ARG VERSION=dev
RUN CGO_ENABLED=1 go build -trimpath -ldflags "-s -w -X main.Version=${VERSION}" -o /out/whatsapp-bridge .

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tzdata \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --uid 10001 --create-home bridge \
 && mkdir -p /data/store && chown -R bridge /data
COPY --from=build /out/whatsapp-bridge /usr/local/bin/whatsapp-bridge
USER bridge
WORKDIR /data
ENV WHATSAPP_STORE_DIR=/data/store \
    WHATSAPP_BRIDGE_ADDR=0.0.0.0:8080
VOLUME ["/data/store"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD ["/usr/local/bin/whatsapp-bridge", "-healthcheck"]
ENTRYPOINT ["/usr/local/bin/whatsapp-bridge"]
