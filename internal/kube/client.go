package kube

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kubesqueeze/kubesqueezeagent/internal/domain"
	prom "github.com/kubesqueeze/kubesqueezeagent/internal/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type Client struct {
	set        kubernetes.Interface
	prometheus *prom.Client
	clusterID  string
}

func New(clusterID, kubeconfig, prometheusURL string) (*Client, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		path := kubeconfig
		if path == "" {
			home, _ := os.UserHomeDir()
			path = filepath.Join(home, ".kube", "config")
		}
		config, err = clientcmd.BuildConfigFromFlags("", path)
		if err != nil {
			return nil, fmt.Errorf("create kubernetes config: %w", err)
		}
	}
	config.UserAgent = "kubesqueeze-agent/dev"
	set, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &Client{set: set, prometheus: prom.New(prometheusURL), clusterID: clusterID}, nil
}

func (c *Client) Discover(ctx context.Context) ([]domain.Workload, domain.ClusterSummary, error) {
	deployments, err := c.set.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, domain.ClusterSummary{}, err
	}
	statefulSets, err := c.set.AppsV1().StatefulSets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, domain.ClusterSummary{}, err
	}
	hpas, err := c.set.AutoscalingV2().HorizontalPodAutoscalers("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, domain.ClusterSummary{}, err
	}
	pdbs, err := c.set.PolicyV1().PodDisruptionBudgets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, domain.ClusterSummary{}, err
	}
	nodes, err := c.set.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, domain.ClusterSummary{}, err
	}
	version, err := c.set.Discovery().ServerVersion()
	if err != nil {
		return nil, domain.ClusterSummary{}, err
	}

	hpaTargets := map[string]bool{}
	for _, hpa := range hpas.Items {
		hpaTargets[targetKey(hpa)] = true
	}
	pdbList := pdbs.Items

	workloads := make([]domain.Workload, 0, len(deployments.Items)+len(statefulSets.Items))
	for i := range deployments.Items {
		item := &deployments.Items[i]
		workloads = append(workloads, c.fromDeployment(ctx, item, hpaTargets, pdbList))
	}
	for i := range statefulSets.Items {
		item := &statefulSets.Items[i]
		workloads = append(workloads, c.fromStatefulSet(ctx, item, hpaTargets, pdbList))
	}

	summary := domain.ClusterSummary{Version: version.GitVersion, NodeCount: len(nodes.Items)}
	for _, node := range nodes.Items {
		summary.AllocatableCPUMilli += node.Status.Allocatable.Cpu().MilliValue()
		summary.AllocatableMemoryBytes += node.Status.Allocatable.Memory().Value()
	}
	return workloads, summary, nil
}

func (c *Client) Deployment(ctx context.Context, namespace, name string) (*appsv1.Deployment, error) {
	return c.set.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
}

func (c *Client) SetDeploymentReplicas(ctx context.Context, namespace, name string, replicas int32) (*appsv1.Deployment, error) {
	deployment, err := c.Deployment(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	deployment.Spec.Replicas = &replicas
	return c.set.AppsV1().Deployments(namespace).Update(ctx, deployment, metav1.UpdateOptions{})
}

func (c *Client) WaitDeployment(ctx context.Context, namespace, name string, replicas int32) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		deployment, err := c.Deployment(ctx, namespace, name)
		if err != nil {
			return err
		}
		if replicas == 0 && deployment.Status.Replicas == 0 {
			return nil
		}
		if replicas > 0 && deployment.Status.ObservedGeneration >= deployment.Generation && deployment.Status.AvailableReplicas >= replicas {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *Client) fromDeployment(ctx context.Context, deployment *appsv1.Deployment, hpas map[string]bool, pdbs []policyv1.PodDisruptionBudget) domain.Workload {
	replicas := int32(1)
	if deployment.Spec.Replicas != nil {
		replicas = *deployment.Spec.Replicas
	}
	cpu, memory := requests(deployment.Spec.Template.Spec.Containers, replicas)
	p95, coverage := c.prometheus.P95CPU(ctx, deployment.Namespace, deployment.Name)
	pdbDisruptions, pdbDesired := matchingPDB(deployment.Namespace, deployment.Spec.Selector, replicas, pdbs)
	return domain.Workload{
		ClusterID: c.clusterID, ResourceUID: string(deployment.UID), Namespace: deployment.Namespace,
		Kind: "Deployment", Name: deployment.Name, Environment: environment(deployment.Namespace, deployment.Labels),
		Replicas: replicas, ReadyReplicas: deployment.Status.ReadyReplicas, CPURequestMilli: cpu,
		MemoryRequestBytes: memory, HasHPA: hpas[fmt.Sprintf("%s/Deployment/%s", deployment.Namespace, deployment.Name)],
		PDBDisruptions: pdbDisruptions, PDBDesiredHealthy: pdbDesired, MetricP95CPU: p95,
		MetricCoverage: coverage, Labels: clone(deployment.Labels), Annotations: clone(deployment.Annotations), CollectedAt: time.Now().UTC(),
	}
}

func (c *Client) fromStatefulSet(ctx context.Context, statefulSet *appsv1.StatefulSet, hpas map[string]bool, pdbs []policyv1.PodDisruptionBudget) domain.Workload {
	replicas := int32(1)
	if statefulSet.Spec.Replicas != nil {
		replicas = *statefulSet.Spec.Replicas
	}
	cpu, memory := requests(statefulSet.Spec.Template.Spec.Containers, replicas)
	p95, coverage := c.prometheus.P95CPU(ctx, statefulSet.Namespace, statefulSet.Name)
	pdbDisruptions, pdbDesired := matchingPDB(statefulSet.Namespace, statefulSet.Spec.Selector, replicas, pdbs)
	return domain.Workload{
		ClusterID: c.clusterID, ResourceUID: string(statefulSet.UID), Namespace: statefulSet.Namespace,
		Kind: "StatefulSet", Name: statefulSet.Name, Environment: environment(statefulSet.Namespace, statefulSet.Labels),
		Replicas: replicas, ReadyReplicas: statefulSet.Status.ReadyReplicas, CPURequestMilli: cpu,
		MemoryRequestBytes: memory, HasHPA: hpas[fmt.Sprintf("%s/StatefulSet/%s", statefulSet.Namespace, statefulSet.Name)],
		PDBDisruptions: pdbDisruptions, PDBDesiredHealthy: pdbDesired, MetricP95CPU: p95,
		MetricCoverage: coverage, Labels: clone(statefulSet.Labels), Annotations: clone(statefulSet.Annotations), CollectedAt: time.Now().UTC(),
	}
}

func targetKey(hpa autoscalingv2.HorizontalPodAutoscaler) string {
	return fmt.Sprintf("%s/%s/%s", hpa.Namespace, hpa.Spec.ScaleTargetRef.Kind, hpa.Spec.ScaleTargetRef.Name)
}

func matchingPDB(namespace string, selector *metav1.LabelSelector, replicas int32, pdbs []policyv1.PodDisruptionBudget) (*int32, *int32) {
	workloadSelector, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil, nil
	}
	for _, pdb := range pdbs {
		if pdb.Namespace != namespace || pdb.Spec.Selector == nil {
			continue
		}
		pdbSelector, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err == nil && selectorsOverlap(workloadSelector, pdbSelector) {
			disruptions := pdb.Status.DisruptionsAllowed
			desired := pdb.Status.DesiredHealthy
			if desired == 0 && pdb.Spec.MinAvailable != nil {
				if computed, err := intstr.GetScaledValueFromIntOrPercent(pdb.Spec.MinAvailable, int(replicas), true); err == nil {
					desired = int32(computed)
				}
			}
			return &disruptions, &desired
		}
	}
	return nil, nil
}

func selectorsOverlap(a, b labels.Selector) bool {
	requirements, selectable := a.Requirements()
	if !selectable {
		return false
	}
	set := labels.Set{}
	for _, requirement := range requirements {
		values := requirement.Values().List()
		if len(values) == 1 {
			set[requirement.Key()] = values[0]
		}
	}
	return b.Matches(set)
}

func requests(containers []corev1.Container, replicas int32) (int64, int64) {
	var cpu, memory int64
	for _, container := range containers {
		cpu += container.Resources.Requests.Cpu().MilliValue()
		memory += container.Resources.Requests.Memory().Value()
	}
	return cpu * int64(replicas), memory * int64(replicas)
}

func environment(namespace string, objectLabels map[string]string) string {
	if value := objectLabels["environment"]; value != "" {
		return value
	}
	switch {
	case namespace == "production" || strings.HasPrefix(namespace, "prod"):
		return "production"
	case strings.HasPrefix(namespace, "preview"):
		return "preview"
	case namespace == "development" || strings.HasPrefix(namespace, "dev"):
		return "development"
	default:
		return "unknown"
	}
}

func clone(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
