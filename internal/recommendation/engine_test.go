package recommendation

import (
	"testing"

	"github.com/kubesqueeze/kubesqueezeagent/internal/domain"
	"github.com/kubesqueeze/kubesqueezeagent/internal/policy"
)

func TestBuildSafetyAndSizingRules(t *testing.T) {
	p95 := 0.62
	desiredHealthy := int32(3)

	tests := []struct {
		name       string
		mutate     func(*domain.Workload)
		status     string
		reason     string
		target     int32
		actionType string
	}{
		{
			name: "abandoned preview scales to zero",
			mutate: func(workload *domain.Workload) {
				workload.Environment = "preview"
				workload.Replicas = 3
			},
			status: "proposed", target: 0, actionType: "scale-to-zero",
		},
		{
			name: "oversized development deployment is reduced",
			mutate: func(workload *domain.Workload) {
				workload.Environment = "development"
				workload.Replicas = 5
				workload.CPURequestMilli = 2500
			},
			status: "proposed", target: 2, actionType: "scale-replicas",
		},
		{
			name: "pdb invariant rejects otherwise valid reduction",
			mutate: func(workload *domain.Workload) {
				workload.Environment = "development"
				workload.Replicas = 5
				workload.CPURequestMilli = 2500
				workload.PDBDesiredHealthy = &desiredHealthy
			},
			status: "rejected", reason: "pdb-or-availability-violation", target: 5, actionType: "scale-replicas",
		},
		{
			name: "production is always rejected",
			mutate: func(workload *domain.Workload) {
				workload.Environment = "production"
			},
			status: "rejected", reason: "production-policy-deny", target: 5,
		},
		{
			name: "hpa owned replicas are rejected",
			mutate: func(workload *domain.Workload) {
				workload.HasHPA = true
			},
			status: "rejected", reason: "hpa-managed", target: 5,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workload := domain.Workload{
				ClusterID: "cluster_demo", ResourceUID: "uid-1", Namespace: "development",
				Kind: "Deployment", Name: "checkout-api", Environment: "development",
				Replicas: 5, ReadyReplicas: 5, CPURequestMilli: 2500,
				MetricP95CPU: &p95, MetricCoverage: 1, Labels: map[string]string{},
			}
			test.mutate(&workload)

			got := Build(workload)
			if got.Status != test.status || got.ReasonCode != test.reason || got.TargetReplicas != test.target || got.ActionType != test.actionType {
				t.Fatalf("Build() = status %q, reason %q, target %d, action %q; want %q, %q, %d, %q",
					got.Status, got.ReasonCode, got.TargetReplicas, got.ActionType,
					test.status, test.reason, test.target, test.actionType)
			}
			if got.PlanHash == "" {
				t.Fatal("Build() returned an empty plan hash")
			}
		})
	}
}

func TestBuildForPolicyUsesActiveVersionAndRules(t *testing.T) {
	p95 := 0.2
	workload := domain.Workload{
		ClusterID: "cluster_demo", ResourceUID: "uid-1", Namespace: "development",
		Kind: "Deployment", Name: "checkout-api", Environment: "development",
		Replicas: 4, ReadyReplicas: 4, CPURequestMilli: 2000,
		MetricP95CPU: &p95, MetricCoverage: 1, Labels: map[string]string{},
	}
	draft := policy.Draft{
		Timezone: "America/Chicago",
		Environments: map[string]policy.EnvironmentPolicy{
			"development": {AllowedActions: []string{"scale-replicas"}, MinimumReplicas: 2},
		},
		MinimumMetricCoverage: 0.9, RequireHumanApproval: true, NeverModifyStatefulSets: true,
	}

	got := BuildForPolicy(workload, &draft, 7)
	if got.Status != "proposed" || got.TargetReplicas != 2 || got.PolicyVersion != 7 {
		t.Fatalf("BuildForPolicy() = status %q, target %d, policy %d; want proposed, 2, 7", got.Status, got.TargetReplicas, got.PolicyVersion)
	}

	got = BuildForPolicy(workload, nil, 0)
	if got.Status != "rejected" || got.ReasonCode != "no-active-policy" || got.PolicyVersion != 0 {
		t.Fatalf("BuildForPolicy(nil) = status %q, reason %q, policy %d", got.Status, got.ReasonCode, got.PolicyVersion)
	}
}
