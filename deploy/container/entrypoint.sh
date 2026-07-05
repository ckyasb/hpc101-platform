#!/bin/sh
# Container entrypoint — inject SSH key then start sshd.
# /home/student is a persistent Docker volume that survives restarts.
# Keys accumulate: new keys are appended, existing keys are preserved.

set -e

if [ -z "$HPC101_SSH_KEY" ]; then
    echo "ERROR: HPC101_SSH_KEY is empty or not set" >&2
    exit 1
fi

mkdir -p /home/student/.ssh
touch /home/student/.ssh/authorized_keys

# Only add the key if it is not already present.
if ! grep -qF "$HPC101_SSH_KEY" /home/student/.ssh/authorized_keys; then
    echo "$HPC101_SSH_KEY" >> /home/student/.ssh/authorized_keys
fi

chmod 0700 /home/student/.ssh
chmod 0600 /home/student/.ssh/authorized_keys
chown -R student:student /home/student/.ssh /home/student

exec /usr/sbin/sshd -D -e -f /etc/ssh/sshd_config
