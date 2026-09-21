# Throwaway image for the VPS agent's end-to-end gate.
#
# Built with network access (plain docker build, no privileges); the test
# itself then runs with --network none --cap-add NET_ADMIN, so nothing the
# agent does can reach anything outside the container.
#
# ARG so the same Dockerfile can be built against debian:bookworm-slim (Debian
# 12) for a one-off check; trixie stays the default used by run.sh.
ARG DEBIAN_CODENAME=trixie
FROM debian:${DEBIAN_CODENAME}-slim
RUN apt-get update -qq \
 && apt-get install -y -qq nftables wireguard-tools iproute2 curl procps python3 \
 && rm -rf /var/lib/apt/lists/*
