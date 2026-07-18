#!/usr/bin/env bash
# Starts three mock backends with DIFFERENT speeds.
#
# They are deliberately heterogeneous: alpha and beta are fast, gamma is slow.
# That asymmetry is the whole point of the project. Round-robin will send gamma
# exactly one third of the traffic no matter how slow it gets, and in week 2 you
# will watch least-connections and P2C refuse to do that.
set -euo pipefail

mkdir -p logs

./bin/mockbackend -name alpha -addr :9001 -delay 10ms -jitter 5ms  > logs/alpha.log 2>&1 &
./bin/mockbackend -name beta  -addr :9002 -delay 10ms -jitter 5ms  > logs/beta.log  2>&1 &
./bin/mockbackend -name gamma -addr :9003 -delay 80ms -jitter 20ms > logs/gamma.log 2>&1 &

sleep 0.5
echo "started:"
echo "  alpha  :9001  ~10ms  (fast)"
echo "  beta   :9002  ~10ms  (fast)"
echo "  gamma  :9003  ~80ms  (slow)"
echo
echo "logs in ./logs/   stop them with: make stop"
