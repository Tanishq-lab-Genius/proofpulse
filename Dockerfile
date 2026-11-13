# Build the Bifrost dashboard assets with the Node version required by Bifrost.
FROM node:26-bookworm-slim AS node

# Build Bifrost and the ProofPulse plugin with the exact same Go toolchain.
FROM golang:1.27.3-bookworm AS builder
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential ca-certificates curl git make \
    && rm -rf /var/lib/apt/lists/*
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -sfn ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -sfn ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx
ENV PATH="/usr/local/bin:${PATH}"

ARG BIFROST_REF=transports/v2.5.0
ARG BIFROST_VERSION=2.5.0
RUN git clone --depth 1 --branch "${BIFROST_REF}" \
      https://github.com/maximhq/bifrost.git /src/bifrost
WORKDIR /src/bifrost
RUN make build DYNAMIC=1 VERSION=${BIFROST_VERSION}

COPY . /src/proofpulse
WORKDIR /src/proofpulse
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
      -o /out/proofpulse ./cmd/proofpulse
WORKDIR /src/proofpulse/bifrost-plugin
RUN CGO_ENABLED=1 go build -a -trimpath -buildmode=plugin \
      -o /out/proofpulse.so .

# Keep only the two runtime binaries and the small runtime libraries.
FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends \
      bash ca-certificates curl libsqlite3-0 libstdc++6 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=builder /src/bifrost/tmp/bifrost-http /usr/local/bin/bifrost-http
COPY --from=builder /out/proofpulse /usr/local/bin/proofpulse
COPY --from=builder /out/proofpulse.so /opt/bifrost/plugins/proofpulse.so
COPY docker/bifrost-config.template.json /opt/bifrost/config.template.json
COPY docker/entrypoint.sh /usr/local/bin/proofpulse-entrypoint
RUN chmod +x /usr/local/bin/proofpulse-entrypoint \
    && mkdir -p /data/bifrost
VOLUME ["/data/bifrost"]
EXPOSE 8000
ENTRYPOINT ["/usr/local/bin/proofpulse-entrypoint"]
