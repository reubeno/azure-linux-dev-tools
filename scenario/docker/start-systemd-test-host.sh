#!/bin/bash
set -euo pipefail

chown -R testuser:testgroup /workdir
export container=docker
exec /sbin/init --log-target=console
