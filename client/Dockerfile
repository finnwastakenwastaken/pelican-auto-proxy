# Pelican Auto Proxy tunnel client, container flavour.
# Runs with network_mode: host and cap_add: NET_ADMIN (see compose.yml):
# the WireGuard interface and nft tables must live in the host's kernel
# network namespace so a container restart never disconnects players.
#
# Unlike the systemd flavour, "install" is skipped here: `run` reads
# AUTOPROXY_JOIN_CODE from the environment and writes /etc/autoproxy and
# /etc/wireguard itself on first start (see client/autoproxy-client, cmd_run).
#
# python3 is deliberately not installed here (image size): the container
# always uses the pure-bash join-code decoder. See client/README.md.

FROM alpine:3.22

RUN apk add --no-cache \
      wireguard-tools \
      nftables \
      iproute2 \
      bash \
      curl \
      ca-certificates \
    && rm -rf /var/cache/apk/*

# Tells the client it runs as the container flavour: it reports "docker" to the
# agent, and "autoproxy-client update" refuses (an image cannot replace itself;
# pull a new image instead).
ENV AUTOPROXY_FLAVOUR=docker

# The release number the image reports ("autoproxy-client version", "status",
# and the version it reports to the VPS). The release workflow passes the tag's
# version; a plain "docker build" without it says "dev", like a source checkout.
# Stamped the same way scripts/package-client.sh stamps the release tarball, and
# the build fails when the one line it replaces is missing.
ARG AUTOPROXY_VERSION=dev

COPY autoproxy-client /usr/local/bin/autoproxy-client
RUN set -eu; \
    v="${AUTOPROXY_VERSION#v}"; \
    if [ "$v" != "dev" ]; then \
      echo "$v" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' \
        || { echo "AUTOPROXY_VERSION '$v' is not X.Y.Z or X.Y.Z-suffix" >&2; exit 1; }; \
      [ "$(grep -cxF 'readonly AUTOPROXY_VERSION="dev"' /usr/local/bin/autoproxy-client)" = 1 ] \
        || { echo 'autoproxy-client must contain exactly one line: readonly AUTOPROXY_VERSION="dev"' >&2; exit 1; }; \
      sed -i "s/^readonly AUTOPROXY_VERSION=\"dev\"\$/readonly AUTOPROXY_VERSION=\"${v}\"/" /usr/local/bin/autoproxy-client; \
    fi; \
    grep -qxF "readonly AUTOPROXY_VERSION=\"${v}\"" /usr/local/bin/autoproxy-client \
      || { echo "stamping version ${v} into autoproxy-client failed" >&2; exit 1; }; \
    chmod 0755 /usr/local/bin/autoproxy-client

LABEL org.opencontainers.image.title="autoproxy-client" \
      org.opencontainers.image.description="Pelican Auto Proxy tunnel client (Docker flavour)" \
      org.opencontainers.image.version="${AUTOPROXY_VERSION}"

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD autoproxy-client status

ENTRYPOINT ["autoproxy-client"]
CMD ["run"]
