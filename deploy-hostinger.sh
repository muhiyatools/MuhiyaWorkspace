#!/usr/bin/env bash
# =============================================================================
# MuhiyaLLM Gateway - Automated 1-Click Deployment Script for Hostinger VPS
# =============================================================================
# Usage:
#   bash deploy-hostinger.sh
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${SCRIPT_DIR}"

echo "================================================================="
echo ">>> [1/4] Checking environment configuration (.env)..."
echo "================================================================="

if [ ! -f ".env" ]; then
    if [ -f ".env.example" ]; then
        echo "[+] Creating .env from .env.example..."
        cp .env.example .env
    else
        echo "[-] Error: .env file not found. Please create one."
        exit 1
    fi
fi

COMPOSE_FILE="docker-compose.hostinger.yml"

echo "================================================================="
echo ">>> [2/4] Starting Redis with password protection..."
echo "================================================================="
docker compose -f "${COMPOSE_FILE}" up -d redis

echo "================================================================="
echo ">>> [3/4] Building and launching MuhiyaLLM Gateway..."
echo "================================================================="
docker compose -f "${COMPOSE_FILE}" build gateway
docker compose -f "${COMPOSE_FILE}" up -d gateway

echo "================================================================="
echo ">>> [4/4] Verifying Gateway Health Status..."
echo "================================================================="
sleep 3

for i in {1..15}; do
    if curl -fsS http://localhost:8090/health >/dev/null 2>&1; then
        echo ">>> MuhiyaLLM Gateway is HEALTHY and RUNNING on port 8090!"
        echo "================================================================="
        docker compose -f "${COMPOSE_FILE}" ps
        exit 0
    fi
    echo "    Waiting for Gateway (attempt $i/15)..."
    sleep 2
done

echo "[-] Warning: Gateway health check did not pass in 30s. Displaying logs:"
docker compose -f "${COMPOSE_FILE}" logs --tail=40 gateway
exit 1
