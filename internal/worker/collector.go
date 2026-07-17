package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/kubesqueeze/kubesqueezeagent/internal/config"
	"github.com/kubesqueeze/kubesqueezeagent/internal/database"
	"github.com/kubesqueeze/kubesqueezeagent/internal/domain"
	"github.com/kubesqueeze/kubesqueezeagent/internal/kube"
	"github.com/kubesqueeze/kubesqueezeagent/internal/policy"
	"github.com/kubesqueeze/kubesqueezeagent/internal/recommendation"
)

type Collector struct {
	config config.Config
	db     *sql.DB
	kube   *kube.Client
}

func NewCollector(cfg config.Config, db *sql.DB, kubeClient *kube.Client) *Collector {
	return &Collector{config: cfg, db: db, kube: kubeClient}
}

func (c *Collector) Run(ctx context.Context) error {
	if err := c.collectAndAnalyze(ctx, "startup"); err != nil {
		slog.Error("initial collection failed", "error", err)
	}
	collectionTicker := time.NewTicker(c.config.CollectorPeriod)
	pollTicker := time.NewTicker(c.config.WorkerPollPeriod)
	defer collectionTicker.Stop()
	defer pollTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-collectionTicker.C:
			if err := c.collectAndAnalyze(ctx, "scheduled"); err != nil {
				slog.Error("scheduled collection failed", "error", err)
			}
		case <-pollTicker.C:
			job, err := database.ClaimJob(ctx, c.db, "analyze")
			if err != nil {
				slog.Error("claim analysis job", "error", err)
				continue
			}
			if job == nil {
				continue
			}
			err = c.collectAndAnalyze(ctx, "job")
			_ = database.FinishJob(ctx, c.db, job.ID, err)
		}
	}
}

func (c *Collector) collectAndAnalyze(ctx context.Context, source string) error {
	workloads, summary, err := c.kube.Discover(ctx)
	if err != nil {
		return err
	}
	filtered := workloads[:0]
	for _, workload := range workloads {
		if isSystemNamespace(workload.Namespace) {
			continue
		}
		filtered = append(filtered, workload)
	}
	if err := c.storeSnapshot(ctx, filtered, summary); err != nil {
		return err
	}
	activePolicy, policyVersion, err := c.activePolicy(ctx)
	if err != nil {
		return err
	}
	recommendations := make([]*domain.Recommendation, 0, len(filtered))
	for _, workload := range filtered {
		recommendations = append(recommendations, recommendation.BuildForPolicy(workload, activePolicy, policyVersion))
	}
	if err := recommendation.ReplaceOpen(ctx, c.db, c.config.ClusterID, recommendations); err != nil {
		return err
	}
	if err := database.AddAudit(ctx, c.db, c.config.TenantID, c.config.ClusterID, "agent", "collector", "analysis.completed", "cluster", c.config.ClusterID, map[string]any{
		"source": source, "workloads": len(filtered), "recommendations": len(recommendations), "nodes": summary.NodeCount,
	}); err != nil {
		return err
	}
	slog.Info("cluster analysis complete", "workloads", len(filtered), "nodes", summary.NodeCount, "source", source)
	return nil
}

func (c *Collector) activePolicy(ctx context.Context) (*policy.Draft, int, error) {
	var body []byte
	var version int
	err := c.db.QueryRowContext(ctx, `SELECT policy,version FROM policy_versions
		WHERE tenant_id=$1 AND status='active' ORDER BY version DESC LIMIT 1`, c.config.TenantID).Scan(&body, &version)
	if err == sql.ErrNoRows {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("load active policy: %w", err)
	}
	var draft policy.Draft
	if err := json.Unmarshal(body, &draft); err != nil {
		return nil, 0, fmt.Errorf("decode active policy version %d: %w", version, err)
	}
	if err := draft.Validate(); err != nil {
		return nil, 0, fmt.Errorf("validate active policy version %d: %w", version, err)
	}
	return &draft, version, nil
}

func (c *Collector) storeSnapshot(ctx context.Context, workloads []domain.Workload, summary domain.ClusterSummary) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "DELETE FROM workload_snapshots WHERE cluster_id=$1", c.config.ClusterID); err != nil {
		return err
	}
	for _, workload := range workloads {
		labels, _ := json.Marshal(workload.Labels)
		_, err := tx.ExecContext(ctx, `INSERT INTO workload_snapshots(
			cluster_id,resource_uid,namespace,kind,name,environment,replicas,ready_replicas,cpu_request_milli,
			memory_request_bytes,has_hpa,pdb_disruptions_allowed,pdb_desired_healthy,metric_p95_cpu_cores,metric_coverage,labels,collected_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`, workload.ClusterID,
			workload.ResourceUID, workload.Namespace, workload.Kind, workload.Name, workload.Environment,
			workload.Replicas, workload.ReadyReplicas, workload.CPURequestMilli, workload.MemoryRequestBytes,
			workload.HasHPA, workload.PDBDisruptions, workload.PDBDesiredHealthy, workload.MetricP95CPU, workload.MetricCoverage, labels, workload.CollectedAt)
		if err != nil {
			return fmt.Errorf("store workload %s/%s: %w", workload.Namespace, workload.Name, err)
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE clusters SET status='connected',kubernetes_version=$2,node_count=$3,
		allocatable_cpu_milli=$4,allocatable_memory_bytes=$5,last_collected_at=now() WHERE id=$1`,
		c.config.ClusterID, summary.Version, summary.NodeCount, summary.AllocatableCPUMilli, summary.AllocatableMemoryBytes)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func isSystemNamespace(namespace string) bool {
	switch namespace {
	case "kube-system", "kube-public", "kube-node-lease", "local-path-storage", "kubesqueeze-system", "default":
		return true
	default:
		return false
	}
}
