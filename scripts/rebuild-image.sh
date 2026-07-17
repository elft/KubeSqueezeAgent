#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CLUSTER_NAME=${KIND_CLUSTER_NAME:-kubesqueeze}
APP_IMAGE=${KUBESQUEEZE_IMAGE:-kubesqueeze:dev}

docker build -t "$APP_IMAGE" "$ROOT"
kind load docker-image "$APP_IMAGE" --name "$CLUSTER_NAME"
kubectl -n kubesqueeze-system rollout restart deployment/kubesqueeze-server deployment/kubesqueeze-collector deployment/kubesqueeze-executor
kubectl -n kubesqueeze-system rollout status deployment/kubesqueeze-server --timeout=180s
echo "Image rebuilt and platform deployments restarted."
