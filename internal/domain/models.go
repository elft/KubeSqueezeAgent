package domain

import "time"

type Workload struct {
	ClusterID          string            `json:"clusterId"`
	ResourceUID        string            `json:"resourceUid"`
	Namespace          string            `json:"namespace"`
	Kind               string            `json:"kind"`
	Name               string            `json:"name"`
	Environment        string            `json:"environment"`
	Replicas           int32             `json:"replicas"`
	ReadyReplicas      int32             `json:"readyReplicas"`
	CPURequestMilli    int64             `json:"cpuRequestMilli"`
	MemoryRequestBytes int64             `json:"memoryRequestBytes"`
	HasHPA             bool              `json:"hasHpa"`
	PDBDisruptions     *int32            `json:"pdbDisruptionsAllowed,omitempty"`
	PDBDesiredHealthy  *int32            `json:"pdbDesiredHealthy,omitempty"`
	MetricP95CPU       *float64          `json:"metricP95CpuCores,omitempty"`
	MetricCoverage     float64           `json:"metricCoverage"`
	Labels             map[string]string `json:"labels"`
	Annotations        map[string]string `json:"annotations"`
	CollectedAt        time.Time         `json:"collectedAt"`
}

type ClusterSummary struct {
	Version                string `json:"version"`
	NodeCount              int    `json:"nodeCount"`
	AllocatableCPUMilli    int64  `json:"allocatableCpuMilli"`
	AllocatableMemoryBytes int64  `json:"allocatableMemoryBytes"`
}

type Recommendation struct {
	ID                      string         `json:"id"`
	ClusterID               string         `json:"clusterId"`
	ResourceUID             string         `json:"resourceUid"`
	Namespace               string         `json:"namespace"`
	WorkloadKind            string         `json:"workloadKind"`
	WorkloadName            string         `json:"workloadName"`
	ActionType              string         `json:"actionType"`
	Status                  string         `json:"status"`
	CurrentReplicas         int32          `json:"currentReplicas"`
	TargetReplicas          int32          `json:"targetReplicas"`
	PotentialMonthlySavings float64        `json:"potentialMonthlySavings"`
	ReasonCode              string         `json:"reasonCode,omitempty"`
	Explanation             string         `json:"explanation"`
	Evidence                map[string]any `json:"evidence"`
	PlanHash                string         `json:"planHash"`
	PolicyVersion           int            `json:"policyVersion"`
	CreatedAt               time.Time      `json:"createdAt"`
	UpdatedAt               time.Time      `json:"updatedAt"`
}

type ChartPoint struct {
	Timestamp int64   `json:"timestamp"`
	Value     float64 `json:"value"`
}

type AuditEvent struct {
	ID         int64          `json:"id"`
	ActorType  string         `json:"actorType"`
	ActorID    string         `json:"actorId"`
	EventType  string         `json:"eventType"`
	ObjectType string         `json:"objectType"`
	ObjectID   string         `json:"objectId"`
	Detail     map[string]any `json:"detail"`
	CreatedAt  time.Time      `json:"createdAt"`
}
