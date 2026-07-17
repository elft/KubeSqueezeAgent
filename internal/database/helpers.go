package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

type Job struct {
	ID        string
	ClusterID string
	Kind      string
	Payload   map[string]any
}

func NewID(prefix string) string {
	raw := make([]byte, 10)
	_, _ = rand.Read(raw)
	return prefix + "_" + hex.EncodeToString(raw)
}

func AddAudit(ctx context.Context, db *sql.DB, tenantID, clusterID, actorType, actorID, eventType, objectType, objectID string, detail any) error {
	body, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO audit_events(tenant_id, cluster_id, actor_type, actor_id, event_type, object_type, object_id, detail)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, tenantID, nullable(clusterID), actorType, actorID, eventType, objectType, objectID, body)
	return err
}

func EnqueueJob(ctx context.Context, db *sql.DB, clusterID, kind string, payload any) (string, error) {
	id := NewID("job")
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	_, err = db.ExecContext(ctx, "INSERT INTO jobs(id,cluster_id,kind,payload) VALUES($1,$2,$3,$4)", id, clusterID, kind, body)
	return id, err
}

func ClaimJob(ctx context.Context, db *sql.DB, kinds ...string) (*Job, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	query := `SELECT id,cluster_id,kind,payload FROM jobs
		WHERE status='pending' AND kind = ANY($1)
		ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`
	var job Job
	var body []byte
	err = tx.QueryRowContext(ctx, query, kinds).Scan(&job.ID, &job.ClusterID, &job.Kind, &body)
	if err == sql.ErrNoRows {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &job.Payload); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE jobs SET status='running',started_at=now() WHERE id=$1", job.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &job, nil
}

func FinishJob(ctx context.Context, db *sql.DB, id string, jobErr error) error {
	if jobErr == nil {
		_, err := db.ExecContext(ctx, "UPDATE jobs SET status='completed',completed_at=now() WHERE id=$1", id)
		return err
	}
	_, err := db.ExecContext(ctx, "UPDATE jobs SET status='failed',error=$2,completed_at=now() WHERE id=$1", id, jobErr.Error())
	return err
}

func WaitForMigrations(ctx context.Context, db *sql.DB) error {
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		var exists bool
		err := db.QueryRowContext(ctx, "SELECT to_regclass('public.jobs') IS NOT NULL").Scan(&exists)
		if err == nil && exists {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("timed out waiting for database migrations")
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
