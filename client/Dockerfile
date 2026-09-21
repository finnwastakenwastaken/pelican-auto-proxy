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

COPY autoproxy-client /usr/local/bin/autoproxy-client
RUN chmod 0755 /usr/local/bin/autoproxy-client

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD autoproxy-client status

ENTRYPOINT ["autoproxy-client"]
CMD ["run"]
