INSERT INTO tenants (id, name, deployment_mode) VALUES
    ('tenant-acme', 'Acme Preview Labs', 'hosted'),
    ('tenant-northstar', 'Northstar Health Systems', 'on-prem')
ON CONFLICT (id) DO NOTHING;

INSERT INTO clusters (id, tenant_id, name, provider, status) VALUES
    ('cluster-acme-kind', 'tenant-acme', 'Acme Interview Demo', 'kind', 'connecting'),
    ('cluster-northstar-private', 'tenant-northstar', 'Northstar Private Cluster', 'on-prem', 'offline')
ON CONFLICT (id) DO NOTHING;

INSERT INTO policy_versions (id, tenant_id, version, status, policy, source_text, approved_by, approved_at) VALUES
(
    'policy-acme-v1', 'tenant-acme', 1, 'active',
    '{"timezone":"America/Chicago","production":{"allowedActions":[]},"development":{"allowedActions":["schedule-sleep","scale-replicas"]},"exclusions":[{"labels":{"customer-demo":"true"}},{"labels":{"app":"payment-simulator"}}],"minimumReplicas":1,"planTTL":"2h"}',
    'Development can shut down after 7 PM, but keep payment simulations running and never modify workloads labeled customer-demo=true.',
    'platform-lead@acme.example', now()
),
(
    'policy-northstar-v1', 'tenant-northstar', 1, 'active',
    '{"timezone":"America/New_York","production":{"allowedActions":[]},"statefulSets":{"automatic":false},"requiredApprovals":2,"metricHistoryDays":30}',
    'Keep all data on premises. Never automatically change StatefulSets or production.',
    'sre-director@northstar.example', now()
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO audit_events (tenant_id, cluster_id, actor_type, actor_id, event_type, object_type, object_id, detail) VALUES
('tenant-acme', 'cluster-acme-kind', 'system', 'seed', 'tenant.created', 'tenant', 'tenant-acme', '{"message":"Seeded Acme customer"}'),
('tenant-acme', 'cluster-acme-kind', 'human', 'platform-lead@acme.example', 'policy.activated', 'policy', 'policy-acme-v1', '{"version":1}'),
('tenant-northstar', 'cluster-northstar-private', 'system', 'seed', 'tenant.created', 'tenant', 'tenant-northstar', '{"message":"Seeded Northstar customer"}');
