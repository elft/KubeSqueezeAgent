# KubeSqueeze Agent

KubeSqueeze is a runnable Kubernetes cost-optimization demo built around
deterministic discovery, planning, human approval, execution, verification,
and rollback.

## Run the complete stack

With Docker, Kind, kubectl, and curl installed:

```bash
make kind-up
```

Open <http://127.0.0.1:8080>. The command builds the application image and
deploys the React dashboard, Go server, read-only collector, scoped executor,
Postgres, Prometheus, seven days of seeded utilization history, and customer-
like workload fixtures into a four-node Kind cluster.

Use `make kind-down` to remove the cluster.

## Documentation

- [Development, debugging, fixtures, and commands](docs/development.md)
