# Per-container sshd image for hpc101-platform student environments.
# Runs OpenSSH on port 2222 with pubkey auth, sftp, and TTY.
# Student authorized keys injected by controller at container start.
#
# Usage:
#   docker build -f deploy/container/Dockerfile -t hpc101-platform/container:latest .
FROM alpine:3.21
RUN apk add --no-cache openssh-server openssh-keygen curl bash && \
    ssh-keygen -A && \
    adduser -D -s /bin/bash student && \
    mkdir -p /run/sshd && \
    chown student:student /run/sshd && \
    chown student:student /etc/ssh/ssh_host_*_key
COPY deploy/container/sshd_config /etc/ssh/sshd_config
COPY deploy/container/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 2222
USER student
ENTRYPOINT ["/entrypoint.sh"]
