#!/usr/bin/env bash
# Sends 12 requests through the balancer and prints which backend served each.
# With round_robin you should see a perfect alpha/beta/gamma rotation.
set -euo pipefail

echo "12 requests through http://127.0.0.1:8080/"
echo
for i in $(seq 1 12); do
  backend=$(curl -s -o /dev/null -D - http://127.0.0.1:8080/ \
    | tr -d '\r' | awk -F': ' '/^X-Gobalance-Backend/ {print $2}')
  printf "  request %2d -> %s\n" "$i" "${backend:-<no response>}"
done

echo
echo "pool state:"
curl -s http://127.0.0.1:8081/backends
