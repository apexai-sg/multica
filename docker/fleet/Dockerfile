# syntax=docker/dockerfile:1
# Repository-root context. Building/pulling/running remains separately authorized.
FROM golang:1.26.9-bookworm AS builder
WORKDIR /src
COPY server/ ./
RUN CGO_ENABLED=0 go build -o /out/fleet ./cmd/fleet

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --gid 10001 fleet && useradd --uid 10001 --gid 10001 --no-create-home fleet
COPY --from=builder /out/fleet /usr/local/bin/fleet
USER 10001:10001
# Operator mounts explicit public config, private service-key/profile files and Docker socket.
# DATABASE_URL selects an already migrated DB; the process never executes DDL.
ENV FLEET_ADDR=0.0.0.0:8090
EXPOSE 8090
ENTRYPOINT ["/usr/local/bin/fleet"]
HEALTHCHECK --interval=5s --timeout=10s CMD ["/usr/local/bin/fleet","readyz"]
