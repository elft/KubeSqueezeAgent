package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kubesqueeze/kubesqueezeagent/internal/config"
	"github.com/kubesqueeze/kubesqueezeagent/internal/database"
	"github.com/kubesqueeze/kubesqueezeagent/internal/domain"
	"github.com/kubesqueeze/kubesqueezeagent/internal/kube"
)

type Executor struct {
	config config.Config
	db     *sql.DB
	kube   *kube.Client
}

func NewExecutor(cfg config.Config, db *sql.DB, kubeClient *kube.Client) *Executor {
	return &Executor{config: cfg, db: db, kube: kubeClient}
}

func (e *Executor) Run(ctx context.Context) error {
	ticker := time.NewTicker(e.config.WorkerPollPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			job, err := database.ClaimJob(ctx, e.db, "execute", "restore")
			if err != nil {
				slog.Error("claim execution job", "error", err)
				continue
			}
			if job == nil {
				continue
			}
			switch job.Kind {
			case "execute":
				err = e.execute(ctx, stringValue(job.Payload, "recommendationId"))
			case "restore":
				err = e.restore(ctx, stringValue(job.Payload, "executionId"), "manual restore")
			}
			if err != nil {
				slog.Error("execution job failed", "job", job.ID, "error", err)
			}
			_ = database.FinishJob(ctx, e.db, job.ID, err)
		}
	}
}

func (e *Executor) execute(ctx context.Context, recommendationID string) error {
	recommendation, err := e.loadRecommendation(ctx, recommendationID)
	if err != nil {
		return err
	}
	if recommendation.Status != "queued" && recommendation.Status != "approved" {
		return fmt.Errorf("recommendation %s is %s, not approved", recommendation.ID, recommendation.Status)
	}
	var approvedHash string
	if err := e.db.QueryRowContext(ctx, "SELECT plan_hash FROM approvals WHERE recommendation_id=$1 ORDER BY approved_at DESC LIMIT 1", recommendation.ID).Scan(&approvedHash); err != nil {
		return fmt.Errorf("approval missing: %w", err)
	}
	if approvedHash != recommendation.PlanHash {
		return errors.New("approval plan hash no longer matches")
	}
	if recommendation.WorkloadKind != "Deployment" {
		return fmt.Errorf("execution for %s is not supported", recommendation.WorkloadKind)
	}

	deployment, err := e.kube.Deployment(ctx, recommendation.Namespace, recommendation.WorkloadName)
	if err != nil {
		return err
	}
	if string(deployment.UID) != recommendation.ResourceUID {
		return errors.New("resource UID changed after approval")
	}
	current := int32(1)
	if deployment.Spec.Replicas != nil {
		current = *deployment.Spec.Replicas
	}
	if current != recommendation.CurrentReplicas {
		return fmt.Errorf("replica precondition changed from %d to %d", recommendation.CurrentReplicas, current)
	}

	executionID := database.NewID("exec")
	if _, err := e.db.ExecContext(ctx, `INSERT INTO executions(id,recommendation_id,status,previous_replicas,target_replicas)
		VALUES($1,$2,'executing',$3,$4)`, executionID, recommendation.ID, current, recommendation.TargetReplicas); err != nil {
		return err
	}
	_, _ = e.db.ExecContext(ctx, "UPDATE recommendations SET status='executing',updated_at=now() WHERE id=$1", recommendation.ID)
	e.step(ctx, executionID, 1, "preflight", "succeeded", map[string]any{"uid": recommendation.ResourceUID, "replicas": current, "planHash": recommendation.PlanHash})
	if _, err := e.kube.SetDeploymentReplicas(ctx, recommendation.Namespace, recommendation.WorkloadName, recommendation.TargetReplicas); err != nil {
		e.failExecution(ctx, executionID, recommendation.ID, err)
		return err
	}
	e.step(ctx, executionID, 2, "squeeze", "succeeded", map[string]any{"from": current, "to": recommendation.TargetReplicas})

	verifyCtx, cancel := context.WithTimeout(ctx, e.config.VerifyTimeout)
	err = e.kube.WaitDeployment(verifyCtx, recommendation.Namespace, recommendation.WorkloadName, recommendation.TargetReplicas)
	cancel()
	if err == nil && deployment.Annotations["demo.kubesqueeze.io/simulate-health-failure"] == "true" {
		err = errors.New("simulated checkout SLO health check failed")
	}
	if err != nil {
		e.step(ctx, executionID, 3, "verify", "failed", map[string]any{"error": err.Error()})
		if rollbackErr := e.rollback(ctx, executionID, recommendation, current, err.Error()); rollbackErr != nil {
			return fmt.Errorf("verification failed (%v), rollback failed: %w", err, rollbackErr)
		}
		return nil
	}

	e.step(ctx, executionID, 3, "verify", "succeeded", map[string]any{"availableReplicas": recommendation.TargetReplicas})
	_, _ = e.db.ExecContext(ctx, "UPDATE executions SET status='succeeded',completed_at=now() WHERE id=$1", executionID)
	_, _ = e.db.ExecContext(ctx, "UPDATE recommendations SET status='succeeded',updated_at=now() WHERE id=$1", recommendation.ID)
	_ = database.AddAudit(ctx, e.db, e.config.TenantID, e.config.ClusterID, "agent", "executor", "execution.succeeded", "execution", executionID, map[string]any{
		"recommendationId": recommendation.ID, "namespace": recommendation.Namespace, "workload": recommendation.WorkloadName,
		"from": current, "to": recommendation.TargetReplicas,
	})
	return nil
}

func (e *Executor) restore(ctx context.Context, executionID, reason string) error {
	var recommendationID, namespace, workloadName string
	var previousReplicas int32
	err := e.db.QueryRowContext(ctx, `SELECT e.recommendation_id,e.previous_replicas,r.namespace,r.workload_name
		FROM executions e JOIN recommendations r ON r.id=e.recommendation_id WHERE e.id=$1`, executionID).
		Scan(&recommendationID, &previousReplicas, &namespace, &workloadName)
	if err != nil {
		return err
	}
	if _, err := e.kube.SetDeploymentReplicas(ctx, namespace, workloadName, previousReplicas); err != nil {
		return err
	}
	verifyCtx, cancel := context.WithTimeout(ctx, e.config.VerifyTimeout)
	err = e.kube.WaitDeployment(verifyCtx, namespace, workloadName, previousReplicas)
	cancel()
	if err != nil {
		return err
	}
	_, _ = e.db.ExecContext(ctx, "UPDATE executions SET status='rolled_back',rollback_reason=$2,completed_at=now() WHERE id=$1", executionID, reason)
	_, _ = e.db.ExecContext(ctx, "UPDATE recommendations SET status='rolled_back',updated_at=now() WHERE id=$1", recommendationID)
	_ = database.AddAudit(ctx, e.db, e.config.TenantID, e.config.ClusterID, "agent", "executor", "execution.rolled_back", "execution", executionID, map[string]any{"reason": reason, "restoredReplicas": previousReplicas})
	return nil
}

func (e *Executor) rollback(ctx context.Context, executionID string, recommendation *domain.Recommendation, previous int32, reason string) error {
	e.step(ctx, executionID, 4, "restore", "running", map[string]any{"to": previous})
	if _, err := e.kube.SetDeploymentReplicas(ctx, recommendation.Namespace, recommendation.WorkloadName, previous); err != nil {
		e.step(ctx, executionID, 4, "restore", "failed", map[string]any{"error": err.Error()})
		return err
	}
	verifyCtx, cancel := context.WithTimeout(ctx, e.config.VerifyTimeout)
	err := e.kube.WaitDeployment(verifyCtx, recommendation.Namespace, recommendation.WorkloadName, previous)
	cancel()
	if err != nil {
		e.step(ctx, executionID, 4, "restore", "failed", map[string]any{"error": err.Error()})
		return err
	}
	e.step(ctx, executionID, 4, "restore", "succeeded", map[string]any{"restoredReplicas": previous})
	_, _ = e.db.ExecContext(ctx, "UPDATE executions SET status='rolled_back',rollback_reason=$2,completed_at=now() WHERE id=$1", executionID, reason)
	_, _ = e.db.ExecContext(ctx, "UPDATE recommendations SET status='rolled_back',updated_at=now() WHERE id=$1", recommendation.ID)
	_ = database.AddAudit(ctx, e.db, e.config.TenantID, e.config.ClusterID, "agent", "executor", "execution.rolled_back", "execution", executionID, map[string]any{"reason": reason, "restoredReplicas": previous})
	return nil
}

func (e *Executor) loadRecommendation(ctx context.Context, id string) (*domain.Recommendation, error) {
	var recommendation domain.Recommendation
	var evidence []byte
	var reason sql.NullString
	err := e.db.QueryRowContext(ctx, `SELECT id,cluster_id,resource_uid,namespace,workload_kind,workload_name,action_type,status,
		current_replicas,target_replicas,potential_monthly_savings,reason_code,explanation,evidence,plan_hash,policy_version,created_at,updated_at
		FROM recommendations WHERE id=$1`, id).Scan(&recommendation.ID, &recommendation.ClusterID, &recommendation.ResourceUID,
		&recommendation.Namespace, &recommendation.WorkloadKind, &recommendation.WorkloadName, &recommendation.ActionType,
		&recommendation.Status, &recommendation.CurrentReplicas, &recommendation.TargetReplicas, &recommendation.PotentialMonthlySavings,
		&reason, &recommendation.Explanation, &evidence, &recommendation.PlanHash, &recommendation.PolicyVersion,
		&recommendation.CreatedAt, &recommendation.UpdatedAt)
	if err != nil {
		return nil, err
	}
	recommendation.ReasonCode = reason.String
	_ = json.Unmarshal(evidence, &recommendation.Evidence)
	return &recommendation, nil
}

func (e *Executor) step(ctx context.Context, executionID string, sequence int, name, status string, detail any) {
	body, _ := json.Marshal(detail)
	_, _ = e.db.ExecContext(ctx, `INSERT INTO execution_steps(execution_id,sequence,name,status,detail) VALUES($1,$2,$3,$4,$5)`, executionID, sequence, name, status, body)
	_ = database.AddAudit(ctx, e.db, e.config.TenantID, e.config.ClusterID, "agent", "executor", "execution.step."+name+"."+status, "execution", executionID, detail)
}

func (e *Executor) failExecution(ctx context.Context, executionID, recommendationID string, err error) {
	_, _ = e.db.ExecContext(ctx, "UPDATE executions SET status='failed',rollback_reason=$2,completed_at=now() WHERE id=$1", executionID, err.Error())
	_, _ = e.db.ExecContext(ctx, "UPDATE recommendations SET status='failed',updated_at=now() WHERE id=$1", recommendationID)
}

func stringValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}
