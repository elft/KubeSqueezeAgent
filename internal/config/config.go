package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL      string
	PrometheusURL    string
	ListenAddress    string
	WebDistDir       string
	ClusterID        string
	TenantID         string
	CollectorPeriod  time.Duration
	WorkerPollPeriod time.Duration
	VerifyTimeout    time.Duration
	Kubeconfig       string
	LLMBaseURL       string
	LLMAPIKey        string
	LLMModel         string
}

func Load() Config {
	return Config{
		DatabaseURL:      env("DATABASE_URL", "postgres://kubesqueeze:kubesqueeze@127.0.0.1:5432/kubesqueeze?sslmode=disable"),
		PrometheusURL:    env("PROMETHEUS_URL", "http://127.0.0.1:9090"),
		ListenAddress:    env("LISTEN_ADDRESS", ":8080"),
		WebDistDir:       env("WEB_DIST_DIR", "web/dist"),
		ClusterID:        env("CLUSTER_ID", "cluster-acme-kind"),
		TenantID:         env("TENANT_ID", "tenant-acme"),
		CollectorPeriod:  duration("COLLECTOR_PERIOD", 45*time.Second),
		WorkerPollPeriod: duration("WORKER_POLL_PERIOD", 2*time.Second),
		VerifyTimeout:    duration("VERIFY_TIMEOUT", 90*time.Second),
		Kubeconfig:       os.Getenv("KUBECONFIG"),
		LLMBaseURL:       os.Getenv("LLM_BASE_URL"),
		LLMAPIKey:        os.Getenv("LLM_API_KEY"),
		LLMModel:         env("LLM_MODEL", "policy-model"),
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func duration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	if parsed, err := time.ParseDuration(value); err == nil {
		return parsed
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		return time.Duration(seconds) * time.Second
	}
	return fallback
}
