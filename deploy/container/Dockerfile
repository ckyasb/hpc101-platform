# Per-container sshd image for hpc101-platform student environments.
# Runs a minimal OpenSSH server on port 2222.
# Student authorized keys are injected by the controller at container start.
#
# Usage:
#   docker build -f deploy/container/Dockerfile -t hpc101-platform/container:latest .
FROM alpine:3.21
RUN apk add --no-cache openssh-server openssh-keygen curl && \
    ssh-keygen -A && \
    adduser -D -s /bin/bash student && \
    mkdir -p /run/sshd /home/student && \
    chown student:student /run/sshd /home/student
COPY deploy/container/sshd_config /etc/ssh/sshd_config
EXPOSE 2222
USER student
ENTRYPOINT ["/usr/sbin/sshd", "-D", "-e", "-f", "/etc/ssh/sshd_config"]
