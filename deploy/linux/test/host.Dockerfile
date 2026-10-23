# A disposable "Linux server" for testing deploy/linux/install.sh and oa-agentctl against a REAL Docker engine
# (Docker-outside-of-Docker: the host's /var/run/docker.sock is mounted). Built and run by docker-host.sh / .ps1.
FROM debian:bookworm-slim
ARG DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends bash ca-certificates curl gnupg openssl iproute2 procps util-linux bsdextrautils \
      expect less tar gzip coreutils findutils grep sed gawk jq file \
 && install -m 0755 -d /etc/apt/keyrings \
 && curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc \
 && chmod a+r /etc/apt/keyrings/docker.asc \
 && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian bookworm stable" >/etc/apt/sources.list.d/docker.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends docker-ce-cli docker-compose-plugin docker-buildx-plugin \
 && rm -rf /var/lib/apt/lists/*
ENV LANG=C.UTF-8 TERM=xterm-256color
WORKDIR /root
CMD ["sleep", "infinity"]
