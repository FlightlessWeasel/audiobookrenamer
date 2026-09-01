#!/bin/sh
set -e

if command -v systemctl >/dev/null; then
    systemctl disable --now audiobookrenamer >/dev/null 2>&1 || true
fi
