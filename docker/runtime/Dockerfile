# syntax=docker/dockerfile:1
# Build context: repository root. Installation/build are separately opt-in.
# Native target-platform stages retain amd64/arm64 support without cross-ABI copies.
FROM golang:1.26.9-bookworm AS builder
WORKDIR /src
COPY server/ ./
RUN CGO_ENABLED=0 go build -o /out/multica ./cmd/multica && CGO_ENABLED=0 go build -o /out/fleet-node ./cmd/fleet-node

FROM node:22-bookworm-slim AS claude
COPY docker/runtime/claude-version.txt /tmp/claude-version.txt
# No build argument can substitute latest or override the checked-in registry pin.
RUN version="$(node -e 'const v=require("fs").readFileSync("/tmp/claude-version.txt","utf8").trim(); if(!/^[0-9]+\.[0-9]+\.[0-9]+$/.test(v)) process.exit(1); process.stdout.write(v)')" \
 && npm install --global --prefix /opt/claude --registry=https://registry.npmjs.org/ --no-audit --no-fund "@anthropic-ai/claude-code@$version"

# A neutral final base must NOT inherit the Node stage's NODE_VERSION/YARN_VERSION.
FROM debian:bookworm-slim AS runtime
# One image serves both the Fleet node and an Aurora task. The Aurora skill
# documents run their own steps in a shell, so the toolchain they call is
# installed here: curl calls the provider and the Multica API, Chromium prints
# the resume PDF, FFmpeg composes captions and extracts audio, ImageMagick
# applies the fixed ID-photo transform, poppler reads PDF text, and jq/unzip
# build request bodies and unpack documents.
# Nothing with a build-time-only purpose enters the image, and the node keeps a
# single launcher entrypoint.
RUN apt-get update && apt-get install -y --no-install-recommends \
      git ca-certificates bash curl jq unzip \
      libstdc++6 libgcc-s1 libatomic1 libnss3 \
      chromium ffmpeg imagemagick poppler-utils fonts-noto-cjk \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --gid 10001 runtime && useradd --uid 10001 --gid 10001 --create-home runtime
COPY --from=claude /usr/local/bin/node /usr/local/bin/node
COPY --from=claude /opt/claude/ /usr/local/
COPY --from=builder /out/ /usr/local/bin/
# Future image builds verify shared-library ABI and the package's actual entrypoint.
RUN ldd /usr/local/bin/node && /usr/local/bin/node --version && /usr/local/bin/claude --version
# Fixed layout owned by the node UID so a fresh named volume mounted at /data or
# /secrets is initialised writable for the non-root node and bootstrap helper.
RUN install -d -o 10001 -g 10001 -m 0700 /data /data/home /data/workspaces /secrets

# The vendored expert tree ships inside this image on purpose: the daemon reads
# AURORA_EXPERTS_DIR when a task claims an expert, and node containers are
# created without bind mounts, so an image-level copy is the only delivery path
# that needs no host mount. A skill update therefore rebuilds this image;
# scripts/aurora-import-experts.mjs --import stays the path for a runtime that is
# populated out of band.
COPY --chown=10001:10001 aurora-experts/ /opt/aurora/experts/
USER 10001:10001
ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin HOME=/data/home AURORA_EXPERTS_DIR=/opt/aurora/experts
ENTRYPOINT ["/usr/local/bin/fleet-node","run"]
HEALTHCHECK --interval=5s --timeout=5s CMD ["/usr/local/bin/fleet-node","health"]
