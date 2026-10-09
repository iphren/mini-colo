#!/usr/bin/env bash

set -euo pipefail

set -a
source .env
set +a

ts=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

power=$(curl -fsS "$POWER_STATUS_URL")

network=$(ssh "$ROUTER_HOST" \
  "vnstat --json m -i '$NETWORK_INTERFACE'")

jq -n \
  --arg timestamp "$ts" \
  --argjson power "$power" \
  --argjson network "$network" \
  '{
    timestamp: $timestamp,
    energy_wh: $power.aenergy.total,
    temperature_c: $power.temperature.tC,
    network_rx_kib: $network.interfaces[0].traffic.total.rx,
    network_tx_kib: $network.interfaces[0].traffic.total.tx
  }'