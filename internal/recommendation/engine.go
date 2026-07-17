package recommendation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/kubesqueeze/kubesqueezeagent/internal/database"
	"github.com/kubesqueeze/kubesqueezeagent/internal/domain"
	"github.com/kubesqueeze/kubesqueezeagent/internal/policy"
)

func Build(workload domain.Workload) *domain.Recommendation {
	draft := defaultPolicy()
	return BuildForPolicy(workload, &draft, 1)
}

func BuildForPolicy(workload domain.Workload, activePolicy *policy.Draft, policyVersion int) *domain.Recommendation {
	recommendation := &domain.Recommendation{
		ID: database.NewID("rec"), ClusterID: workload.ClusterID, ResourceUID: workload.ResourceUID,
		Namespace: workload.Namespace, WorkloadKind: workload.Kind, WorkloadName: workload.Name,
		CurrentReplicas: workload.Replicas, TargetReplicas: workload.Replicas, Status: "rejected",
		PolicyVersion: policyVersion, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		Evidence: map[string]any{
			"environment": workload.Environment, "readyReplicas": workload.ReadyReplicas,
			"cpuRequestMilli": workload.CPURequestMilli, "memoryRequestBytes": workload.MemoryRequestBytes,
			"metricP95CpuCores": workload.MetricP95CPU, "metricCoverage": workload.MetricCoverage,
			"hasHpa": workload.HasHPA, "pdbDisruptionsAllowed": workload.PDBDisruptions,
			"pdbDesiredHealthy": workload.PDBDesiredHealthy,
		},
	}
	if activePolicy == nil {
		recommendation.ReasonCode = "no-active-policy"
		recommendation.Explanation = "Rejected: activate a policy before analysis can propose changes."
		recommendation.PlanHash = planHash(recommendation)
		return recommendation
	}
	rule, environmentAllowed := activePolicy.Environments[workload.Environment]

	switch {
	case excludedByPolicy(workload.Labels, activePolicy.Exclusions):
		recommendation.ReasonCode = "customer-policy-exclusion"
		recommendation.Explanation = "Rejected: this workload matches an explicit customer exclusion in the active policy."
	case workload.Kind == "StatefulSet" && activePolicy.NeverModifyStatefulSets:
		recommendation.ReasonCode = "stateful-opt-in-required"
		recommendation.Explanation = "Rejected: the active policy does not permit changes to StatefulSets."
	case !environmentAllowed || len(rule.AllowedActions) == 0:
		recommendation.ReasonCode = "production-policy-deny"
		recommendation.Explanation = fmt.Sprintf("Rejected: active policy version %d permits no actions in %s.", policyVersion, workload.Environment)
	case workload.HasHPA:
		recommendation.ReasonCode = "hpa-managed"
		recommendation.Explanation = "Rejected: an HPA owns this workload's replica count."
	case workload.MetricP95CPU == nil || workload.MetricCoverage < activePolicy.MinimumMetricCoverage:
		recommendation.ReasonCode = "insufficient-metrics-history"
		recommendation.Explanation = fmt.Sprintf("Rejected: metric coverage must be at least %.0f%%.", activePolicy.MinimumMetricCoverage*100)
	case workload.Environment == "preview" && actionAllowed(rule, "scale-to-zero") && workload.Replicas > rule.MinimumReplicas:
		recommendation.Status = "proposed"
		recommendation.ActionType = "scale-to-zero"
		recommendation.TargetReplicas = rule.MinimumReplicas
		recommendation.ReasonCode = ""
		recommendation.Explanation = fmt.Sprintf("Scale preview workload from %d replicas to %d; seven-day p95 CPU is %.3f cores and active policy version %d allows scale-to-zero.", workload.Replicas, rule.MinimumReplicas, *workload.MetricP95CPU, policyVersion)
	case workload.Environment == "development" && actionAllowed(rule, "scale-replicas") && workload.Replicas > rule.MinimumReplicas:
		perReplicaRequest := float64(workload.CPURequestMilli) / 1000 / float64(workload.Replicas)
		if perReplicaRequest <= 0 {
			recommendation.ReasonCode = "missing-resource-requests"
			recommendation.Explanation = "Rejected: CPU requests are required for a safe replica recommendation."
			break
		}
		target := int32(math.Ceil(*workload.MetricP95CPU / (perReplicaRequest * 0.70)))
		if target < rule.MinimumReplicas {
			target = rule.MinimumReplicas
		}
		if target >= workload.Replicas {
			recommendation.ReasonCode = "already-right-sized"
			recommendation.Explanation = "Rejected: historical demand does not support reducing replicas."
			break
		}
		recommendation.Status = "proposed"
		recommendation.ActionType = "scale-replicas"
		recommendation.TargetReplicas = target
		recommendation.ReasonCode = ""
		recommendation.Explanation = fmt.Sprintf("Reduce from %d replicas to %d; seven-day p95 CPU is %.3f cores with 30%% headroom under active policy version %d.", workload.Replicas, target, *workload.MetricP95CPU, policyVersion)
	default:
		recommendation.ReasonCode = "policy-no-action"
		recommendation.Explanation = "Rejected: no active policy rule permits an action for this workload."
	}

	if recommendation.Status == "proposed" {
		if workload.PDBDesiredHealthy != nil && recommendation.TargetReplicas < *workload.PDBDesiredHealthy {
			recommendation.Status = "rejected"
			recommendation.ReasonCode = "pdb-or-availability-violation"
			recommendation.Explanation = fmt.Sprintf("Rejected: target %d would violate the workload availability invariant of %d healthy replicas.", recommendation.TargetReplicas, *workload.PDBDesiredHealthy)
			recommendation.TargetReplicas = workload.Replicas
		}
	}
	if recommendation.Status == "proposed" {
		freedMilli := float64(workload.CPURequestMilli) * float64(workload.Replicas-recommendation.TargetReplicas) / float64(workload.Replicas)
		recommendation.PotentialMonthlySavings = math.Round((freedMilli/1000*12)*100) / 100
	}
	recommendation.PlanHash = planHash(recommendation)
	return recommendation
}

func actionAllowed(rule policy.EnvironmentPolicy, action string) bool {
	for _, allowed := range rule.AllowedActions {
		if allowed == action {
			return true
		}
	}
	return false
}

func excludedByPolicy(labels map[string]string, exclusions []policy.Exclusion) bool {
	for _, exclusion := range exclusions {
		matches := true
		for key, value := range exclusion.Labels {
			if labels[key] != value {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}

func defaultPolicy() policy.Draft {
	return policy.Draft{
		Timezone: "America/Chicago",
		Environments: map[string]policy.EnvironmentPolicy{
			"production":  {AllowedActions: []string{}, MinimumReplicas: 1},
			"development": {AllowedActions: []string{"scale-replicas", "schedule-sleep"}, MinimumReplicas: 1},
			"preview":     {AllowedActions: []string{"scale-to-zero"}, MinimumReplicas: 0},
		},
		Exclusions: []policy.Exclusion{
			{Labels: map[string]string{"customer-demo": "true"}, Reason: "Customer demos must remain unchanged"},
			{Labels: map[string]string{"app": "payment-simulator"}, Reason: "Payment simulations must remain online"},
		},
		MinimumMetricCoverage: 0.8, RequireHumanApproval: true, NeverModifyStatefulSets: true,
	}
}

func ReplaceOpen(ctx context.Context, db *sql.DB, clusterID string, recommendations []*domain.Recommendation) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "DELETE FROM recommendations WHERE cluster_id=$1 AND status IN ('proposed','rejected')", clusterID); err != nil {
		return err
	}
	for _, recommendation := range recommendations {
		var active bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recommendations
			WHERE cluster_id=$1 AND resource_uid=$2 AND status IN ('approved','queued','executing'))`,
			clusterID, recommendation.ResourceUID).Scan(&active); err != nil {
			return err
		}
		if active {
			continue
		}
		evidence, _ := json.Marshal(recommendation.Evidence)
		_, err := tx.ExecContext(ctx, `INSERT INTO recommendations(
			id,cluster_id,resource_uid,namespace,workload_kind,workload_name,action_type,status,
			current_replicas,target_replicas,potential_monthly_savings,reason_code,explanation,evidence,plan_hash,policy_version)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULLIF($12,''),$13,$14,$15,$16)`,
			recommendation.ID, recommendation.ClusterID, recommendation.ResourceUID, recommendation.Namespace,
			recommendation.WorkloadKind, recommendation.WorkloadName, recommendation.ActionType, recommendation.Status,
			recommendation.CurrentReplicas, recommendation.TargetReplicas, recommendation.PotentialMonthlySavings,
			recommendation.ReasonCode, recommendation.Explanation, evidence, recommendation.PlanHash, recommendation.PolicyVersion)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func planHash(recommendation *domain.Recommendation) string {
	payload := struct {
		UID      string         `json:"uid"`
		Action   string         `json:"action"`
		Current  int32          `json:"current"`
		Target   int32          `json:"target"`
		Policy   int            `json:"policy"`
		Evidence map[string]any `json:"evidence"`
	}{recommendation.ResourceUID, recommendation.ActionType, recommendation.CurrentReplicas, recommendation.TargetReplicas, recommendation.PolicyVersion, recommendation.Evidence}
	body, _ := json.Marshal(payload)
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
