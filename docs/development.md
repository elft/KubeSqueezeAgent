# Development and debugging

## Repository boundaries

```text
cmd/kubesqueeze/             Process entry point and CLI commands
internal/api/                HTTP, SSE, and static React delivery; no Kubernetes client
internal/config/             Environment-based process configuration
internal/database/           Embedded migrations, seed data, jobs, and audit helpers
internal/domain/             Shared typed contracts
internal/kube/               Kubernetes discovery and bounded mutations
internal/prometheus/         Prometheus queries and synthetic history backfill
internal/recommendation/     Deterministic recommendation rules and plan hashes
internal/worker/collector.go Read-only discovery and analysis worker
internal/worker/executor.go  Approved squeeze, verification, and restore worker
internal/workload/           Synthetic live workload and Prometheus exporter
web/                         React, TypeScript, TanStack Query/Table, and Vite
deploy/platform/             Postgres, Prometheus, RBAC, and platform deployments
deploy/fixtures/             Customer-like Kubernetes objects and safety edge cases
scripts/                     Repeatable Kind and debugging workflows
```

The server, collector, and executor are separate Kubernetes deployments. The
server service account has no Kubernetes RBAC. The collector is read-only. The
executor can update Deployments and use the Eviction API, but it cannot read
Secrets or create arbitrary resources.

## Complete Kind environment

Prerequisites:

- Docker with at least 8 GB available memory
- Kind
- kubectl
- curl

When using WSL, Docker Desktop must have integration enabled for the current
distribution. `make kind-up` checks both the CLI and daemon before building.

Run:

```bash
make kind-up
```

This builds `kubesqueeze:dev`, creates a four-node Kind cluster, loads the
image, deploys Postgres and Prometheus, runs migrations and customer seeds,
backfills seven days of synthetic metrics, creates the workload fixtures, and
opens the dashboard on <http://127.0.0.1:8080>.

The deployment is intentionally Helm-independent so it can bootstrap with only
Kind and kubectl. All objects are managed by `deploy/kustomization.yaml`.
CI repeats the same bootstrap against Kubernetes 1.34 and 1.35 Kind node images
instead of maintaining a separate test-only deployment path.

Override the cluster or node image when testing another Kubernetes version:

```bash
KIND_CLUSTER_NAME=kubesqueeze-134 \
KIND_NODE_IMAGE=kindest/node:v1.34.3@sha256:08497ee19eace7b4b5348db5c6a1591d7752b164530a36f855cb0f2bdcbadd48 \
make kind-up
```

Destroy everything with `make kind-down`.

## Seeded environment

The SQL migrations create two tenants, two policies, cluster records, and
initial audit events. The Kind fixture represents Acme Preview Labs and adds:

- abandoned and active preview namespaces
- development Deployments with office-hours, steady, and bursty demand
- protected customer-demo and payment-simulator workloads
- production Deployments and a production PriorityClass
- HPAs and PDBs
- a StatefulSet
- topology spread and anti-affinity constraints
- one workload annotated to simulate a failed post-squeeze SLO

Prometheus accepts a one-time remote-write backfill from the seed Job. The demo
workload pods then continue exposing live values every scrape, so the history
grows naturally while the cluster runs.

## Debug the server and client locally

Keep Postgres and Prometheus in Kind, but replace the server process with a
local debugger:

```bash
make debug-deps
make web-build
make dev-server
```

In another terminal:

```bash
make dev-client
```

Open <http://127.0.0.1:5173>. Vite proxies `/api` to port 8080 and produces
source maps. The VS Code launch file contains Go server, collector, executor,
and browser configurations. Run `make debug-deps` before selecting one.

To avoid a port conflict while debugging the local API server, scale down the
in-cluster server or use kubectl port-forward instead of the Kind host mapping:

```bash
kubectl -n kubesqueeze-system scale deployment/kubesqueeze-server --replicas=0
```

The collector and executor can also run on the host using the current
kubeconfig:

```bash
make dev-collector
make dev-executor
```

Only run one collector and executor at a time when stepping through jobs.

## Rebuild the in-cluster application

```bash
./scripts/rebuild-image.sh
```

This rebuilds and reloads the image, then restarts only the platform
deployments. Existing Postgres and Prometheus history remains intact.

## Exercise squeeze and restore

Approve a proposed plan in the dashboard, then either press **Run squeeze** or
use the CLI:

```bash
./bin/kubesqueeze squeeze \
  --api-url http://127.0.0.1:8080 \
  --recommendation rec_example
```

Restore a successful execution with:

```bash
./bin/kubesqueeze restore \
  --api-url http://127.0.0.1:8080 \
  --execution exec_example
```

The `rollback-demo` recommendation deliberately fails verification after the
scale operation. The executor automatically restores its previous replica
count and records all four steps and the rollback reason.

## Useful diagnostics

```bash
kubectl -n kubesqueeze-system get pods,jobs
kubectl -n kubesqueeze-system logs deployment/kubesqueeze-collector -f
kubectl -n kubesqueeze-system logs deployment/kubesqueeze-executor -f
kubectl get deployments,statefulsets,hpa,pdb -A
curl http://127.0.0.1:8080/api/healthz
curl http://127.0.0.1:8080/api/v1/recommendations
```

Prometheus is reachable after `make debug-deps` at
<http://127.0.0.1:9090>. Query `demo_workload_cpu_cores` to inspect seeded and
live series.

## Optional BYO model

The platform runs without an LLM. To enable the policy onboarding tab, set the
following only on `deployment/kubesqueeze-server`:

```text
LLM_BASE_URL=https://your-openai-compatible-endpoint/v1
LLM_MODEL=your-policy-model
LLM_API_KEY=load-this-from-a-Kubernetes-Secret
```

For the Kind environment, pass the values when deploying. `LLM_API_KEY` is
stored in the `kubesqueeze-llm` Secret and all three settings are applied only
to the server deployment:

```bash
LLM_BASE_URL=https://your-openai-compatible-endpoint/v1 \
LLM_MODEL=your-policy-model \
LLM_API_KEY=your-api-key \
make kind-up
```

The base URL must be reachable from inside the Kind cluster; `127.0.0.1`
inside the server pod refers to the pod itself. With Docker Desktop, use
`http://host.docker.internal:11434/v1` for an Ollama-compatible endpoint
running on the host. Confirm the effective settings without printing the
secret:

```bash
kubectl -n kubesqueeze-system exec deployment/kubesqueeze-server -- \
  sh -c 'printf "%s\n%s\n" "$LLM_BASE_URL" "$LLM_MODEL"'
curl http://127.0.0.1:8080/api/v1/llm
```

The adapter only calls the OpenAI-compatible `chat/completions` contract. This
works with hosted gateways and with on-prem inference servers that implement
that contract. Provider output is decoded into `internal/policy.Draft`, checked
against deterministic invariants, and stored as an inactive policy version.
The dashboard uses the provider's streaming response to show draft JSON as it
arrives. The streamed preview is not persisted unless the completed response
passes typed decoding and all safety checks.

This path is not RAG: there is no embedding model, vector database, document
index, or retrieval step. Cluster state and Prometheus history feed the
deterministic recommendation engine directly; they are not inserted into the
model prompt. A future RAG path could retrieve approved policy examples or
tenant documentation for drafting, but retrieved text would still need to pass
the same deterministic validator.

Inactive policy drafts and superseded policies can be removed from the policy
tab. Deletion is scoped to the configured tenant and creates an audit event.
Active policies are protected from deletion until a separate lifecycle action
supersedes or deactivates them.

There is deliberately no model path to activate policy, approve a plan, access
the Kubernetes API, or invoke squeeze/restore. External agentic bots can use
the same approval-aware HTTP API and `X-KubeSqueeze-Actor` attribution rather
than being granted cluster credentials.
