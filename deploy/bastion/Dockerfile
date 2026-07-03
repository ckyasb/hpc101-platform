# Bastion container image for hpc101-platform
# Alpine with pre-installed OpenSSH server, configured for cert-only auth.
# Runs sshd as non-root on port 2222 (setcap required for binding).
#
# Usage:
#   docker build -f deploy/bastion/Dockerfile -t hpc101-platform/bastion:latest .
FROM alpine:3.21
RUN apk add --no-cache openssh-server openssh-keygen && \
    ssh-keygen -A && \
    adduser -D -s /bin/false bastion && \
    mkdir -p /run/sshd /etc/ssh && \
    chown bastion:bastion /run/sshd
COPY deploy/bastion/sshd_config /etc/ssh/sshd_config
EXPOSE 2222
USER bastion
ENTRYPOINT ["/usr/sbin/sshd", "-D", "-e", "-f", "/etc/ssh/sshd_config"]
