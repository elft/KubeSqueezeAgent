package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kubesqueeze/kubesqueezeagent/internal/config"
	"github.com/kubesqueeze/kubesqueezeagent/internal/database"
	"github.com/kubesqueeze/kubesqueezeagent/internal/domain"
	"github.com/kubesqueeze/kubesqueezeagent/internal/llm"
	prom "github.com/kubesqueeze/kubesqueezeagent/internal/prometheus"
)

type Server struct {
	config     config.Config
	db         *sql.DB
	prometheus *prom.Client
	llm        *llm.Client
}

func New(cfg config.Config, db *sql.DB) *Server {
	return &Server{config: cfg, db: db, prometheus: prom.New(cfg.PrometheusURL), llm: llm.New(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel)}
}

func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/healthz", s.health)
	mux.HandleFunc("GET /api/v1/summary", s.summary)
	mux.HandleFunc("GET /api/v1/workloads", s.workloads)
	mux.HandleFunc("GET /api/v1/recommendations", s.recommendations)
	mux.HandleFunc("POST /api/v1/analyze", s.analyze)
	mux.HandleFunc("POST /api/v1/recommendations/{id}/approve", s.approve)
	mux.HandleFunc("POST /api/v1/recommendations/{id}/execute", s.execute)
	mux.HandleFunc("GET /api/v1/executions", s.executions)
	mux.HandleFunc("POST /api/v1/executions/{id}/restore", s.restore)
	mux.HandleFunc("GET /api/v1/audit", s.audit)
	mux.HandleFunc("GET /api/v1/events", s.events)
	mux.HandleFunc("GET /api/v1/history", s.history)
	mux.HandleFunc("GET /api/v1/policies", s.policies)
	mux.HandleFunc("GET /api/v1/llm", s.llmStatus)
	mux.HandleFunc("POST /api/v1/policies/draft", s.draftPolicy)
	mux.HandleFunc("POST /api/v1/policies/draft/stream", s.streamDraftPolicy)
	mux.HandleFunc("POST /api/v1/policies/{id}/activate", s.activatePolicy)
	mux.HandleFunc("POST /api/v1/policies/{id}/deactivate", s.deactivatePolicy)
	mux.HandleFunc("DELETE /api/v1/policies/{id}", s.deletePolicy)
	s.mountWeb(mux)

	server := &http.Server{Addr: s.config.ListenAddress, Handler: requestLog(cors(mux)), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	slog.Info("api server listening", "address", s.config.ListenAddress, "web", s.config.WebDistDir)
	err := server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	status := map[string]any{"status": "ok", "database": "ok", "prometheus": "ok", "time": time.Now().UTC()}
	code := http.StatusOK
	if err := s.db.PingContext(r.Context()); err != nil {
		status["database"] = err.Error()
		status["status"] = "degraded"
		code = http.StatusServiceUnavailable
	}
	if err := s.prometheus.Ready(r.Context()); err != nil {
		status["prometheus"] = err.Error()
		status["status"] = "degraded"
	}
	writeJSON(w, code, status)
}

func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	result := map[string]any{}
	err := s.db.QueryRowContext(r.Context(), `SELECT name,status,COALESCE(kubernetes_version,''),node_count,
		allocatable_cpu_milli,allocatable_memory_bytes,last_collected_at FROM clusters WHERE id=$1`, s.config.ClusterID).
		Scan(mapScanner(result, "clusterName", "clusterStatus", "kubernetesVersion", "nodeCount", "allocatableCpuMilli", "allocatableMemoryBytes", "lastCollectedAt")...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var workloads, proposed, rejected, rollbacks int
	var potential float64
	_ = s.db.QueryRowContext(r.Context(), "SELECT count(*) FROM workload_snapshots WHERE cluster_id=$1", s.config.ClusterID).Scan(&workloads)
	_ = s.db.QueryRowContext(r.Context(), "SELECT count(*),COALESCE(sum(potential_monthly_savings),0) FROM recommendations WHERE cluster_id=$1 AND status IN ('proposed','approved','queued')", s.config.ClusterID).Scan(&proposed, &potential)
	_ = s.db.QueryRowContext(r.Context(), "SELECT count(*) FROM recommendations WHERE cluster_id=$1 AND status='rejected'", s.config.ClusterID).Scan(&rejected)
	_ = s.db.QueryRowContext(r.Context(), "SELECT count(*) FROM executions WHERE status='rolled_back'").Scan(&rollbacks)
	result["workloads"] = workloads
	result["proposedRecommendations"] = proposed
	result["rejectedRecommendations"] = rejected
	result["potentialMonthlySavings"] = potential
	result["rollbacks"] = rollbacks
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) workloads(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `SELECT cluster_id,resource_uid,namespace,kind,name,environment,replicas,
		ready_replicas,cpu_request_milli,memory_request_bytes,has_hpa,pdb_disruptions_allowed,pdb_desired_healthy,metric_p95_cpu_cores,
		metric_coverage,labels,collected_at FROM workload_snapshots WHERE cluster_id=$1 ORDER BY environment,namespace,name`, s.config.ClusterID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	defer rows.Close()
	items := []domain.Workload{}
	for rows.Next() {
		var item domain.Workload
		var labelsJSON []byte
		if err := rows.Scan(&item.ClusterID, &item.ResourceUID, &item.Namespace, &item.Kind, &item.Name, &item.Environment,
			&item.Replicas, &item.ReadyReplicas, &item.CPURequestMilli, &item.MemoryRequestBytes, &item.HasHPA,
			&item.PDBDisruptions, &item.PDBDesiredHealthy, &item.MetricP95CPU, &item.MetricCoverage, &labelsJSON, &item.CollectedAt); err != nil {
			writeError(w, 500, err)
			return
		}
		_ = json.Unmarshal(labelsJSON, &item.Labels)
		items = append(items, item)
	}
	writeJSON(w, 200, items)
}

func (s *Server) recommendations(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,cluster_id,resource_uid,namespace,workload_kind,workload_name,
		action_type,status,current_replicas,target_replicas,potential_monthly_savings,reason_code,explanation,evidence,
		plan_hash,policy_version,created_at,updated_at FROM recommendations WHERE cluster_id=$1 ORDER BY
		CASE WHEN status='proposed' THEN 0 WHEN status='approved' THEN 1 WHEN status='rejected' THEN 2 ELSE 3 END,created_at DESC`, s.config.ClusterID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	defer rows.Close()
	items := []domain.Recommendation{}
	for rows.Next() {
		var item domain.Recommendation
		var reason sql.NullString
		var evidence []byte
		if err := rows.Scan(&item.ID, &item.ClusterID, &item.ResourceUID, &item.Namespace, &item.WorkloadKind,
			&item.WorkloadName, &item.ActionType, &item.Status, &item.CurrentReplicas, &item.TargetReplicas,
			&item.PotentialMonthlySavings, &reason, &item.Explanation, &evidence, &item.PlanHash, &item.PolicyVersion,
			&item.CreatedAt, &item.UpdatedAt); err != nil {
			writeError(w, 500, err)
			return
		}
		item.ReasonCode = reason.String
		_ = json.Unmarshal(evidence, &item.Evidence)
		items = append(items, item)
	}
	writeJSON(w, 200, items)
}

func (s *Server) analyze(w http.ResponseWriter, r *http.Request) {
	id, err := database.EnqueueJob(r.Context(), s.db, s.config.ClusterID, "analyze", map[string]any{"requestedBy": actor(r)})
	if err != nil {
		writeError(w, 500, err)
		return
	}
	_ = database.AddAudit(r.Context(), s.db, s.config.TenantID, s.config.ClusterID, "human", actor(r), "analysis.requested", "job", id, map[string]any{})
	writeJSON(w, http.StatusAccepted, map[string]any{"jobId": id, "status": "pending"})
}

func (s *Server) approve(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var status, planHash string
	if err := s.db.QueryRowContext(r.Context(), "SELECT status,plan_hash FROM recommendations WHERE id=$1", id).Scan(&status, &planHash); err != nil {
		writeError(w, 404, err)
		return
	}
	if status != "proposed" {
		writeError(w, 409, fmt.Errorf("recommendation is %s", status))
		return
	}
	approvalID := database.NewID("approval")
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err == nil {
		_, err = tx.ExecContext(r.Context(), "INSERT INTO approvals(id,recommendation_id,plan_hash,approved_by) VALUES($1,$2,$3,$4)", approvalID, id, planHash, actor(r))
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), "UPDATE recommendations SET status='approved',updated_at=now() WHERE id=$1", id)
	}
	if err == nil {
		err = tx.Commit()
	} else if tx != nil {
		_ = tx.Rollback()
	}
	if err != nil {
		writeError(w, 500, err)
		return
	}
	_ = database.AddAudit(r.Context(), s.db, s.config.TenantID, s.config.ClusterID, "human", actor(r), "recommendation.approved", "recommendation", id, map[string]any{"planHash": planHash})
	writeJSON(w, 200, map[string]any{"approvalId": approvalID, "status": "approved"})
}

func (s *Server) execute(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	result, err := s.db.ExecContext(r.Context(), "UPDATE recommendations SET status='queued',updated_at=now() WHERE id=$1 AND status='approved'", id)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		writeError(w, 409, fmt.Errorf("recommendation is not approved"))
		return
	}
	jobID, err := database.EnqueueJob(r.Context(), s.db, s.config.ClusterID, "execute", map[string]any{"recommendationId": id})
	if err != nil {
		writeError(w, 500, err)
		return
	}
	_ = database.AddAudit(r.Context(), s.db, s.config.TenantID, s.config.ClusterID, "human", actor(r), "execution.queued", "recommendation", id, map[string]any{"jobId": jobID})
	writeJSON(w, 202, map[string]any{"jobId": jobID, "status": "queued"})
}

func (s *Server) executions(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `SELECT e.id,e.recommendation_id,e.status,e.previous_replicas,e.target_replicas,
		COALESCE(e.rollback_reason,''),e.started_at,e.completed_at,r.namespace,r.workload_name FROM executions e
		JOIN recommendations r ON r.id=e.recommendation_id ORDER BY e.started_at DESC LIMIT 100`)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		item := map[string]any{}
		if err := rows.Scan(mapScanner(item, "id", "recommendationId", "status", "previousReplicas", "targetReplicas", "rollbackReason", "startedAt", "completedAt", "namespace", "workloadName")...); err != nil {
			writeError(w, 500, err)
			return
		}
		items = append(items, item)
	}
	writeJSON(w, 200, items)
}

func (s *Server) restore(w http.ResponseWriter, r *http.Request) {
	executionID := r.PathValue("id")
	jobID, err := database.EnqueueJob(r.Context(), s.db, s.config.ClusterID, "restore", map[string]any{"executionId": executionID})
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 202, map[string]any{"jobId": jobID, "status": "pending"})
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	rows, err := s.db.QueryContext(r.Context(), `SELECT id,actor_type,actor_id,event_type,object_type,object_id,detail,created_at
		FROM audit_events WHERE tenant_id=$1 AND id>$2 ORDER BY id DESC LIMIT 200`, s.config.TenantID, after)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	defer rows.Close()
	items := []domain.AuditEvent{}
	for rows.Next() {
		var item domain.AuditEvent
		var detail []byte
		if err := rows.Scan(&item.ID, &item.ActorType, &item.ActorID, &item.EventType, &item.ObjectType, &item.ObjectID, &detail, &item.CreatedAt); err != nil {
			continue
		}
		_ = json.Unmarshal(detail, &item.Detail)
		items = append(items, item)
	}
	writeJSON(w, 200, items)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, fmt.Errorf("streaming unavailable"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	lastID, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		rows, err := s.db.QueryContext(r.Context(), `SELECT id,event_type,object_id,detail,created_at FROM audit_events
			WHERE tenant_id=$1 AND id>$2 ORDER BY id LIMIT 100`, s.config.TenantID, lastID)
		if err == nil {
			for rows.Next() {
				var id int64
				var eventType, objectID string
				var detail json.RawMessage
				var created time.Time
				if rows.Scan(&id, &eventType, &objectID, &detail, &created) == nil {
					payload, _ := json.Marshal(map[string]any{"id": id, "type": eventType, "objectId": objectID, "detail": detail, "createdAt": created})
					fmt.Fprintf(w, "id: %d\nevent: audit\ndata: %s\n\n", id, payload)
					lastID = id
				}
			}
			rows.Close()
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	namespace := r.URL.Query().Get("namespace")
	workload := r.URL.Query().Get("workload")
	if namespace == "" || workload == "" {
		writeError(w, 400, fmt.Errorf("namespace and workload are required"))
		return
	}
	points, err := s.prometheus.History(r.Context(), namespace, workload, 7*24*time.Hour)
	if err != nil {
		writeError(w, 502, err)
		return
	}
	writeJSON(w, 200, points)
}

func (s *Server) policies(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), "SELECT id,version,status,policy,source_text,approved_by,approved_at,created_at FROM policy_versions WHERE tenant_id=$1 ORDER BY version DESC", s.config.TenantID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		item := map[string]any{}
		var policy json.RawMessage
		var source, approver sql.NullString
		var approvedAt sql.NullTime
		var id, status string
		var version int
		var created time.Time
		if rows.Scan(&id, &version, &status, &policy, &source, &approver, &approvedAt, &created) == nil {
			item = map[string]any{"id": id, "version": version, "status": status, "policy": policy, "sourceText": source.String, "approvedBy": approver.String, "createdAt": created}
			if approvedAt.Valid {
				item["approvedAt"] = approvedAt.Time
			}
			items = append(items, item)
		}
	}
	writeJSON(w, 200, items)
}

func (s *Server) llmStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"enabled": s.llm.Enabled(), "model": s.llm.Model(), "role": "policy-drafting-only"})
}

func (s *Server) draftPolicy(w http.ResponseWriter, r *http.Request) {
	requirements, err := policyRequirements(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	draft, err := s.llm.DraftPolicy(r.Context(), requirements)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	result, err := s.savePolicyDraft(r, requirements, draft)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) streamDraftPolicy(w http.ResponseWriter, r *http.Request) {
	requirements, err := policyRequirements(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming unavailable"))
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	writeEvent := func(value any) error {
		if err := json.NewEncoder(w).Encode(value); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	if err := writeEvent(map[string]any{"type": "status", "status": "generating"}); err != nil {
		return
	}
	draft, err := s.llm.DraftPolicyStream(r.Context(), requirements, func(delta string) error {
		return writeEvent(map[string]any{"type": "delta", "delta": delta})
	})
	if err != nil {
		_ = writeEvent(map[string]any{"type": "error", "error": err.Error()})
		return
	}
	result, err := s.savePolicyDraft(r, requirements, draft)
	if err != nil {
		_ = writeEvent(map[string]any{"type": "error", "error": err.Error()})
		return
	}
	_ = writeEvent(map[string]any{"type": "complete", "draft": result})
}

func (s *Server) savePolicyDraft(r *http.Request, requirements string, draft any) (map[string]any, error) {
	body, err := json.Marshal(draft)
	if err != nil {
		return nil, err
	}
	var version int
	if err := s.db.QueryRowContext(r.Context(), "SELECT COALESCE(max(version),0)+1 FROM policy_versions WHERE tenant_id=$1", s.config.TenantID).Scan(&version); err != nil {
		return nil, err
	}
	id := database.NewID("policy")
	if _, err := s.db.ExecContext(r.Context(), `INSERT INTO policy_versions(id,tenant_id,version,status,policy,source_text)
		VALUES($1,$2,$3,'draft',$4,$5)`, id, s.config.TenantID, version, body, requirements); err != nil {
		return nil, err
	}
	_ = database.AddAudit(r.Context(), s.db, s.config.TenantID, s.config.ClusterID, "human", actor(r), "policy.draft.created", "policy", id, map[string]any{"version": version, "model": s.llm.Model()})
	return map[string]any{"id": id, "version": version, "status": "draft", "policy": draft, "activationRequired": true}, nil
}

func (s *Server) activatePolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	var version int
	var status string
	err = tx.QueryRowContext(r.Context(), `SELECT version,status FROM policy_versions
		WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, id, s.config.TenantID).Scan(&version, &status)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, fmt.Errorf("policy not found"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if status != "draft" {
		writeError(w, http.StatusConflict, fmt.Errorf("only draft policies can be activated; policy is %s", status))
		return
	}

	if _, err = tx.ExecContext(r.Context(), `UPDATE policy_versions SET status='superseded'
		WHERE tenant_id=$1 AND status='active'`, s.config.TenantID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE policy_versions SET status='active',approved_by=$3,approved_at=now()
		WHERE id=$1 AND tenant_id=$2`, id, s.config.TenantID, actor(r)); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE recommendations SET status='expired',updated_at=now()
		WHERE cluster_id=$1 AND status IN ('proposed','approved')`, s.config.ClusterID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	_ = database.AddAudit(r.Context(), s.db, s.config.TenantID, s.config.ClusterID, "human", actor(r), "policy.activated", "policy", id, map[string]any{"version": version})
	jobID, enqueueErr := database.EnqueueJob(r.Context(), s.db, s.config.ClusterID, "analyze", map[string]any{"requestedBy": actor(r), "reason": "policy-activated"})
	if enqueueErr != nil {
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "version": version, "status": "active"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "version": version, "status": "active", "analysisJobId": jobID})
}

func (s *Server) deactivatePolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	var version int
	var status string
	err = tx.QueryRowContext(r.Context(), `SELECT version,status FROM policy_versions
		WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, id, s.config.TenantID).Scan(&version, &status)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, fmt.Errorf("policy not found"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if status != "active" {
		writeError(w, http.StatusConflict, fmt.Errorf("only the active policy can be deactivated; policy is %s", status))
		return
	}

	var inFlight int
	if err = tx.QueryRowContext(r.Context(), `SELECT count(*) FROM recommendations
		WHERE cluster_id=$1 AND policy_version=$2 AND status IN ('queued','executing')`, s.config.ClusterID, version).Scan(&inFlight); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if inFlight > 0 {
		writeError(w, http.StatusConflict, fmt.Errorf("policy has %d in-flight execution(s); wait for them to finish before deactivating", inFlight))
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE policy_versions SET status='superseded'
		WHERE id=$1 AND tenant_id=$2`, id, s.config.TenantID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE recommendations SET status='expired',updated_at=now()
		WHERE cluster_id=$1 AND policy_version=$2 AND status IN ('proposed','approved')`, s.config.ClusterID, version); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	_ = database.AddAudit(r.Context(), s.db, s.config.TenantID, s.config.ClusterID, "human", actor(r), "policy.deactivated", "policy", id, map[string]any{"version": version})
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "version": version, "status": "superseded"})
}

func (s *Server) deletePolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	var status string
	err = tx.QueryRowContext(r.Context(), `SELECT version,status FROM policy_versions
		WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, id, s.config.TenantID).Scan(&version, &status)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, fmt.Errorf("policy not found"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if status == "active" {
		writeError(w, http.StatusConflict, fmt.Errorf("deactivate the active policy before deleting it"))
		return
	}
	if _, err = tx.ExecContext(r.Context(), "DELETE FROM policy_versions WHERE id=$1 AND tenant_id=$2", id, s.config.TenantID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	_ = database.AddAudit(r.Context(), s.db, s.config.TenantID, s.config.ClusterID, "human", actor(r), "policy.deleted", "policy", id, map[string]any{"version": version, "status": status})
	w.WriteHeader(http.StatusNoContent)
}

func policyRequirements(w http.ResponseWriter, r *http.Request) (string, error) {
	var input struct {
		Requirements string `json:"requirements"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 20<<10))
	if err := decoder.Decode(&input); err != nil {
		return "", fmt.Errorf("decode requirements: %w", err)
	}
	input.Requirements = strings.TrimSpace(input.Requirements)
	if len(input.Requirements) < 10 {
		return "", fmt.Errorf("requirements must be at least 10 characters")
	}
	return input.Requirements, nil
}

func (s *Server) mountWeb(mux *http.ServeMux) {
	directory := s.config.WebDistDir
	index := filepath.Join(directory, "index.html")
	if _, err := os.Stat(index); err != nil {
		mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, 200, map[string]any{"name": "KubeSqueeze API", "ui": "run `cd web && npm run dev` for the client"})
		})
		return
	}
	fileServer := http.FileServer(http.Dir(directory))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(directory, filepath.Clean(r.URL.Path))
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, index)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func actor(r *http.Request) string {
	if value := r.Header.Get("X-KubeSqueeze-Actor"); value != "" {
		return value
	}
	return "demo.user@acme.example"
}

func mapScanner(target map[string]any, keys ...string) []any {
	values := make([]any, len(keys))
	for index, key := range keys {
		values[index] = scanner{set: func(value any) { target[key] = value }}
	}
	return values
}

type scanner struct{ set func(any) }

func (s scanner) Scan(src any) error { s.set(src); return nil }

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type,X-KubeSqueeze-Actor")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/healthz" {
			slog.Debug("http request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
		}
	})
}
