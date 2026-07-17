export type Summary = {
  clusterName: string
  clusterStatus: string
  kubernetesVersion: string
  nodeCount: number
  allocatableCpuMilli: number
  allocatableMemoryBytes: number
  lastCollectedAt?: string
  workloads: number
  proposedRecommendations: number
  rejectedRecommendations: number
  potentialMonthlySavings: number
  rollbacks: number
}

export type Workload = {
  resourceUid: string
  namespace: string
  kind: string
  name: string
  environment: string
  replicas: number
  readyReplicas: number
  cpuRequestMilli: number
  memoryRequestBytes: number
  hasHpa: boolean
  pdbDisruptionsAllowed?: number
  pdbDesiredHealthy?: number
  metricP95CpuCores?: number
  metricCoverage: number
  collectedAt: string
}

export type Recommendation = {
  id: string
  namespace: string
  workloadKind: string
  workloadName: string
  actionType: string
  status: string
  currentReplicas: number
  targetReplicas: number
  potentialMonthlySavings: number
  reasonCode?: string
  explanation: string
  evidence: Record<string, unknown>
  planHash: string
	policyVersion: number
}

export type Execution = {
  id: string
  recommendationId: string
  status: string
  previousReplicas: number
  targetReplicas: number
  rollbackReason: string
  namespace: string
  workloadName: string
  startedAt: string
  completedAt?: string
}

export type AuditEvent = {
  id: number
  actorType: string
  actorId: string
  eventType: string
  objectType: string
  objectId: string
  detail: Record<string, unknown>
  createdAt: string
}

export type ChartPoint = { timestamp: number; value: number }

export type LLMStatus = { enabled: boolean; model: string; role: string }

export type PolicyDraft = {
  id: string
  version: number
  status: 'draft'
  activationRequired: true
  policy: Record<string, unknown>
}

export type PolicyVersion = {
  id: string
  version: number
  status: 'draft' | 'active' | 'superseded'
  policy: Record<string, unknown>
  sourceText: string
  approvedBy?: string
  approvedAt?: string
  createdAt: string
}
