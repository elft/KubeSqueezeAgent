#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CLUSTER_NAME=${KIND_CLUSTER_NAME:-kubesqueeze}
NODE_IMAGE=${KIND_NODE_IMAGE:-kindest/node:v1.35.0@sha256:452d707d4862f52530247495d180205e029056831160e22870e37e3f6c1ac31f}
APP_IMAGE=${KUBESQUEEZE_IMAGE:-kubesqueeze:dev}

for command in docker kind kubectl curl; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "Missing required command: $command" >&2
    if [[ "$command" == docker ]]; then
      echo "Docker is not available. In WSL, enable Docker Desktop integration for this distribution." >&2
    fi
    exit 1
  fi
done

if ! docker info >/dev/null 2>&1; then
  echo "Docker is installed but its daemon is unavailable." >&2
  echo "In WSL, enable Docker Desktop integration for this distribution, then rerun make kind-up." >&2
  exit 1
fi

echo "[1/8] Building ${APP_IMAGE}"
docker build -t "$APP_IMAGE" "$ROOT"

if ! kind get clusters 2>/dev/null | grep -qx "$CLUSTER_NAME"; then
  echo "[2/8] Creating Kind cluster ${CLUSTER_NAME} with ${NODE_IMAGE}"
  kind create cluster --name "$CLUSTER_NAME" --image "$NODE_IMAGE" --config "$ROOT/deploy/kind/cluster.yaml"
else
  echo "[2/8] Reusing Kind cluster ${CLUSTER_NAME}"
fi

echo "[3/8] Loading application image"
kind load docker-image "$APP_IMAGE" --name "$CLUSTER_NAME"

echo "[4/8] Applying platform and fixture manifests"
kubectl config use-context "kind-${CLUSTER_NAME}" >/dev/null
kubectl -n kubesqueeze-system delete job kubesqueeze-migrate kubesqueeze-seed-metrics --ignore-not-found >/dev/null 2>&1 || true
kubectl apply -k "$ROOT/deploy"

# The LLM settings are intentionally scoped to the API server. Passing them to
# `make kind-up` previously stopped at this script and never reached the pod.
kubectl -n kubesqueeze-system set env deployment/kubesqueeze-server \
  LLM_BASE_URL="${LLM_BASE_URL:-}" \
  LLM_MODEL="${LLM_MODEL:-policy-model}" >/dev/null

if [[ -n "${LLM_API_KEY:-}" ]]; then
  kubectl -n kubesqueeze-system create secret generic kubesqueeze-llm \
    --from-literal="LLM_API_KEY=${LLM_API_KEY}" \
    --dry-run=client \
    -o yaml | kubectl apply -f - >/dev/null
  kubectl -n kubesqueeze-system set env deployment/kubesqueeze-server \
    --from=secret/kubesqueeze-llm >/dev/null
else
  kubectl -n kubesqueeze-system set env deployment/kubesqueeze-server \
    LLM_API_KEY- >/dev/null
fi

echo "[5/8] Waiting for Postgres and Prometheus"
kubectl -n kubesqueeze-system rollout status statefulset/postgres --timeout=180s
kubectl -n kubesqueeze-system rollout status deployment/prometheus --timeout=180s

echo "[6/8] Waiting for migrations and seven-day metric backfill"
kubectl -n kubesqueeze-system wait --for=condition=complete job/kubesqueeze-migrate --timeout=180s
kubectl -n kubesqueeze-system wait --for=condition=complete job/kubesqueeze-seed-metrics --timeout=180s

echo "[7/8] Waiting for KubeSqueeze and demo workloads"
kubectl -n kubesqueeze-system rollout status deployment/kubesqueeze-server --timeout=180s
kubectl -n kubesqueeze-system rollout status deployment/kubesqueeze-collector --timeout=180s
kubectl -n kubesqueeze-system rollout status deployment/kubesqueeze-executor --timeout=180s
for namespace in development production preview-pr-1842 preview-pr-1901 customer-demos; do
  kubectl -n "$namespace" wait --for=condition=available deployment --all --timeout=240s
done

echo "[8/8] Requesting a fresh analysis"
for _ in $(seq 1 30); do
  if curl -fsS -X POST http://127.0.0.1:8080/api/v1/analyze -H 'X-KubeSqueeze-Actor: bootstrap' >/dev/null; then
    break
  fi
  sleep 2
done
sleep 5

echo
echo "KubeSqueeze is ready: http://127.0.0.1:8080"
echo "Run 'make debug-deps' before debugging server or client processes locally."
echo "Run 'kubectl -n kubesqueeze-system get pods' to inspect the platform."
