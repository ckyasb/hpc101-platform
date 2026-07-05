# Student container image for hpc101-platform.
# Debian 12, OpenSSH on :2222, pubkey-only, + common dev tools.
#
# Build:  docker build -f deploy/container/Dockerfile -t hpc101-platform/container:latest .
# Run:    docker run -e HPC101_SSH_KEY="ssh-ed25519 AAAA..." \
#           -v <student>-home:/home/student hpc101-platform/container:latest
FROM debian:12-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    openssh-server \
    curl wget git \
    build-essential gcc g++ make cmake \
    python3 python3-pip python3-venv \
    vim nano tmux \
    bash-completion \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && ssh-keygen -A \
    && useradd -m -s /bin/bash student \
    && mkdir -p /run/sshd

COPY deploy/container/sshd_config /etc/ssh/sshd_config
COPY deploy/container/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 2222
USER student
ENTRYPOINT ["/entrypoint.sh"]
