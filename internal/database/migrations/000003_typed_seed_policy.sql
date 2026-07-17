UPDATE policy_versions
SET policy = '{
  "timezone": "America/Chicago",
  "environments": {
    "production": {"allowedActions": [], "minimumReplicas": 1},
    "development": {"allowedActions": ["scale-replicas", "schedule-sleep"], "minimumReplicas": 1, "sleepAfter": "19:00", "wakeAt": "07:00"},
    "preview": {"allowedActions": ["scale-to-zero"], "minimumReplicas": 0}
  },
  "exclusions": [
    {"labels": {"customer-demo": "true"}, "reason": "Customer demos must remain unchanged"},
    {"labels": {"app": "payment-simulator"}, "reason": "Payment simulations must remain online"}
  ],
  "minimumMetricCoverage": 0.8,
  "requireHumanApproval": true,
  "neverModifyStatefulSets": true
}'::jsonb
WHERE id = 'policy-acme-v1';

CREATE UNIQUE INDEX policy_versions_one_active_per_tenant_idx
ON policy_versions (tenant_id)
WHERE status = 'active';
