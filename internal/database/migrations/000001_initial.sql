CREATE TABLE tenants (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    deployment_mode TEXT NOT NULL CHECK (deployment_mode IN ('hosted', 'on-prem')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE clusters (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id),
    name TEXT NOT NULL,
    provider TEXT NOT NULL DEFAULT 'kind',
    status TEXT NOT NULL DEFAULT 'connecting',
    kubernetes_version TEXT,
    node_count INTEGER NOT NULL DEFAULT 0,
    allocatable_cpu_milli BIGINT NOT NULL DEFAULT 0,
    allocatable_memory_bytes BIGINT NOT NULL DEFAULT 0,
    last_collected_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE policy_versions (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id),
    version INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('draft', 'active', 'superseded')),
    policy JSONB NOT NULL,
    source_text TEXT,
    approved_by TEXT,
    approved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, version)
);

CREATE TABLE workload_snapshots (
    cluster_id TEXT NOT NULL REFERENCES clusters(id),
    resource_uid TEXT NOT NULL,
    namespace TEXT NOT NULL,
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    environment TEXT NOT NULL,
    replicas INTEGER NOT NULL,
    ready_replicas INTEGER NOT NULL,
    cpu_request_milli BIGINT NOT NULL,
    memory_request_bytes BIGINT NOT NULL,
    has_hpa BOOLEAN NOT NULL DEFAULT false,
    pdb_disruptions_allowed INTEGER,
    pdb_desired_healthy INTEGER,
    metric_p95_cpu_cores DOUBLE PRECISION,
    metric_coverage DOUBLE PRECISION,
    labels JSONB NOT NULL DEFAULT '{}',
    collected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (cluster_id, resource_uid)
);

CREATE TABLE recommendations (
    id TEXT PRIMARY KEY,
    cluster_id TEXT NOT NULL REFERENCES clusters(id),
    resource_uid TEXT NOT NULL,
    namespace TEXT NOT NULL,
    workload_kind TEXT NOT NULL,
    workload_name TEXT NOT NULL,
    action_type TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('proposed', 'rejected', 'approved', 'queued', 'executing', 'succeeded', 'rolled_back', 'failed', 'expired')),
    current_replicas INTEGER NOT NULL,
    target_replicas INTEGER NOT NULL,
    potential_monthly_savings DOUBLE PRECISION NOT NULL DEFAULT 0,
    reason_code TEXT,
    explanation TEXT NOT NULL,
    evidence JSONB NOT NULL,
    plan_hash TEXT NOT NULL,
    policy_version INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX recommendations_cluster_status_idx ON recommendations(cluster_id, status, created_at DESC);

CREATE TABLE approvals (
    id TEXT PRIMARY KEY,
    recommendation_id TEXT NOT NULL REFERENCES recommendations(id),
    plan_hash TEXT NOT NULL,
    approved_by TEXT NOT NULL,
    approved_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE executions (
    id TEXT PRIMARY KEY,
    recommendation_id TEXT NOT NULL REFERENCES recommendations(id),
    status TEXT NOT NULL,
    previous_replicas INTEGER NOT NULL,
    target_replicas INTEGER NOT NULL,
    rollback_reason TEXT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);

CREATE TABLE execution_steps (
    id BIGSERIAL PRIMARY KEY,
    execution_id TEXT NOT NULL REFERENCES executions(id),
    sequence INTEGER NOT NULL,
    name TEXT NOT NULL,
    status TEXT NOT NULL,
    detail JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    cluster_id TEXT NOT NULL REFERENCES clusters(id),
    kind TEXT NOT NULL CHECK (kind IN ('analyze', 'execute', 'restore')),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'completed', 'failed')),
    payload JSONB NOT NULL DEFAULT '{}',
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ
);
CREATE INDEX jobs_claim_idx ON jobs(kind, status, created_at);

CREATE TABLE audit_events (
    id BIGSERIAL PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    cluster_id TEXT,
    actor_type TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    object_type TEXT NOT NULL,
    object_id TEXT NOT NULL,
    detail JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX audit_events_tenant_idx ON audit_events(tenant_id, id DESC);
