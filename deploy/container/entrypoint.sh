#!/bin/sh
# Container entrypoint: inject authorized SSH key then exec sshd.
# Reads HPC101_SSH_KEY from environment, writes to authorized_keys.

set -e

if [ -z "$HPC101_SSH_KEY" ]; then
    echo "ERROR: HPC101_SSH_KEY is empty or not set" >&2
    exit 1
fi

mkdir -p /home/student/.ssh
echo "$HPC101_SSH_KEY" > /home/student/.ssh/authorized_keys
chmod 0700 /home/student/.ssh
chmod 0600 /home/student/.ssh/authorized_keys
chown -R student:student /home/student/.ssh

exec /usr/sbin/sshd -D -e -f /etc/ssh/sshd_config
