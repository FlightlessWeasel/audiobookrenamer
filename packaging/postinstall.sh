#!/bin/sh
set -e

if ! getent group audiobookrenamer >/dev/null; then
    addgroup --system audiobookrenamer 2>/dev/null || groupadd --system audiobookrenamer
fi
if ! getent passwd audiobookrenamer >/dev/null; then
    adduser --system --no-create-home --ingroup audiobookrenamer \
        --home /var/lib/audiobookrenamer audiobookrenamer 2>/dev/null || \
    useradd --system --no-create-home --gid audiobookrenamer \
        --home-dir /var/lib/audiobookrenamer audiobookrenamer
fi

mkdir -p /var/lib/audiobookrenamer
chown audiobookrenamer:audiobookrenamer /var/lib/audiobookrenamer

if command -v systemctl >/dev/null; then
    systemctl daemon-reload || true
    echo "audiobookrenamer installed. Enable with: systemctl enable --now audiobookrenamer"
fi
