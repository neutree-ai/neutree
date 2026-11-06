package operator

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/neutree-ai/neutree/internal/semver"
)

const (
	NVIDIAAcceleratorType    = "nvidia_gpu"
	NVIDIAOperatorName       = "nvidia"
	NVIDIAClusterPolicyName  = "cluster-policy"
	NVIDIAGPUNodeLabel       = "nvidia.com/gpu.present"
	NVIDIAGPUNodeValue       = "true"
	NVIDIADriverDeployLabel  = "nvidia.com/gpu.deploy.driver"
	NVIDIAToolkitDeployLabel = "nvidia.com/gpu.deploy.container-toolkit"
)

// Compatibility aliases used by renderers and scheduling adapters.
const (
	NvidiaGPUNodeLabel       = NVIDIAGPUNodeLabel
	NvidiaGPUNodeValue       = NVIDIAGPUNodeValue
	NvidiaDriverDeployLabel  = NVIDIADriverDeployLabel
	NvidiaToolkitDeployLabel = NVIDIAToolkitDeployLabel
)

var NVIDIAClusterPolicyGVK = schema.GroupVersionKind{
	Group: "nvidia.com", Version: "v1", Kind: "ClusterPolicy",
}

const (
	controllerRevisionHashLabel = "controller-revision-hash"
	controllerRevisionHashAlt   = "controller.kubernetes.io/hash"
	daemonSetKind               = "DaemonSet"
)

// ProviderManagement describes the effective ClusterPolicy owner selection.
type ProviderManagement struct {
	Managed              bool
	DriverManaged        bool
	ToolkitManaged       bool
	Generation           int64
	OperatorVersion      string
	OperatorVersionKnown bool
	ClientLabelManaged   bool
}

type providerManagement = ProviderManagement

type providerPod struct {
	Pod       corev1.Pod
	DaemonSet appsv1.DaemonSet
	Revision  string
}

// ProviderNodeState exposes the provider boundary for one node. Integrations
// use it to compare base Pod starts without reimplementing provider discovery.
type ProviderNodeState struct {
	Node                       string
	Ready                      bool
	DriverReady                bool
	ProviderLastContainerStart time.Time
	ProviderStartKnown         bool
	Reason                     string
}

type nvidiaNodeState = ProviderNodeState

// NvidiaProvider evaluates ClusterPolicy ownership and current provider Pods.
// It does not install, upgrade, or mutate NVIDIA resources.
type NvidiaProvider struct {
	Client client.Client
}

func NewNvidiaProvider(ctrlClient client.Client) *NvidiaProvider {
	return &NvidiaProvider{Client: ctrlClient}
}

func (p *NvidiaProvider) Management(ctx context.Context) (ProviderManagement, error) {
	if p == nil || p.Client == nil {
		return providerManagement{}, fmt.Errorf("kubernetes client is required")
	}

	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(NVIDIAClusterPolicyGVK)

	err := p.Client.Get(ctx, types.NamespacedName{Name: NVIDIAClusterPolicyName}, policy)
	if apiMeta.IsNoMatchError(err) {
		return providerManagement{}, nil
	}

	if apierrors.IsNotFound(err) {
		policies := &unstructured.UnstructuredList{}
		policies.SetGroupVersionKind(schema.GroupVersionKind{Group: "nvidia.com", Version: "v1", Kind: "ClusterPolicyList"})

		if listErr := p.Client.List(ctx, policies); listErr != nil {
			if apierrors.IsNotFound(listErr) || apiMeta.IsNoMatchError(listErr) {
				return providerManagement{}, nil
			}

			return providerManagement{}, fmt.Errorf("list NVIDIA ClusterPolicy: %w", listErr)
		}

		if len(policies.Items) == 0 {
			return providerManagement{}, nil
		}

		if len(policies.Items) > 1 {
			return providerManagement{}, fmt.Errorf("expected one NVIDIA ClusterPolicy, found %d", len(policies.Items))
		}

		policy = &policies.Items[0]
	} else if err != nil {
		return providerManagement{}, fmt.Errorf("get NVIDIA ClusterPolicy: %w", err)
	}

	driver, err := effectiveEnabled(policy, "driver")
	if err != nil {
		return providerManagement{}, err
	}

	toolkit, err := effectiveEnabled(policy, "toolkit")
	if err != nil {
		return providerManagement{}, err
	}

	version, versionKnown, err := detectOperatorVersion(ctx, p.Client, policy)
	if err != nil {
		return providerManagement{}, err
	}

	clientManaged := false
	if !versionKnown {
		clientManaged = true
	} else {
		older, err := semver.LessThan(version, "v26.7.0")
		if err != nil {
			return providerManagement{}, fmt.Errorf("parse GPU Operator version %q: %w", version, err)
		}

		clientManaged = older
	}

	return providerManagement{
		Managed: driver && toolkit, DriverManaged: driver, ToolkitManaged: toolkit,
		Generation: policy.GetGeneration(), OperatorVersion: version,
		OperatorVersionKnown: versionKnown, ClientLabelManaged: clientManaged,
	}, nil
}

func detectOperatorVersion(ctx context.Context, ctrlClient client.Client, policy *unstructured.Unstructured) (string, bool, error) {
	for _, key := range []string{"app.kubernetes.io/version", "nvidia.com/gpu-operator-version"} {
		if value := strings.TrimSpace(policy.GetLabels()[key]); value != "" {
			return value, true, nil
		}
	}

	pods := &corev1.PodList{}
	if err := ctrlClient.List(ctx, pods); err != nil {
		return "", false, fmt.Errorf("list GPU Operator pods for version: %w", err)
	}

	for i := range pods.Items {
		pod := &pods.Items[i]
		name := pod.Labels["app.kubernetes.io/name"]

		if name == "" {
			name = pod.Labels["app"]
		}

		if name != "gpu-operator" {
			continue
		}

		for _, container := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
			if version := imageVersion(container.Image); version != "" {
				return version, true, nil
			}
		}
	}

	return "", false, nil
}

func imageVersion(image string) string {
	image = strings.Split(image, "@")[0]
	index := strings.LastIndex(image, ":")

	if index < 0 || index == len(image)-1 {
		return ""
	}

	return image[index+1:]
}

func effectiveEnabled(policy *unstructured.Unstructured, component string) (bool, error) {
	value, found, err := unstructured.NestedFieldNoCopy(policy.Object, "spec", component, "enabled")
	if err != nil {
		return false, fmt.Errorf("read ClusterPolicy spec.%s.enabled: %w", component, err)
	}

	if !found {
		return false, nil
	}

	enabled, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("ClusterPolicy spec.%s.enabled must be boolean", component)
	}

	return enabled, nil
}

// Evaluate updates the Neutree label on all NVIDIA GPU nodes. It returns an
// aggregate state; provider start time is the latest node boundary and should
// not be used for per-node stale checks.
func (p *NvidiaProvider) Evaluate(ctx context.Context) (ProviderState, error) {
	management, err := p.Management(ctx)
	if err != nil {
		return ProviderState{}, err
	}

	state := ProviderState{
		Managed: management.Managed, DriverManaged: management.DriverManaged,
		ToolkitManaged: management.ToolkitManaged, PolicyGeneration: management.Generation,
		OperatorVersion: management.OperatorVersion, OperatorVersionKnown: management.OperatorVersionKnown,
		ClientLabelManaged: management.ClientLabelManaged,
		NodeStates:         make(map[string]ProviderNodeState),
	}
	if !management.Managed {
		state.Ready = true
		state.Reason = "GPU Operator does not manage NVIDIA driver and container toolkit together"

		return state, nil
	}

	nodes := &corev1.NodeList{}
	if err := p.Client.List(ctx, nodes, client.MatchingLabels{NVIDIAGPUNodeLabel: NVIDIAGPUNodeValue}); err != nil {
		return ProviderState{}, fmt.Errorf("list NVIDIA GPU nodes: %w", err)
	}

	if len(nodes.Items) == 0 {
		state.Ready = true
		state.Reason = "no NVIDIA GPU nodes"

		return state, nil
	}

	state.Ready = true
	var latest time.Time

	for i := range nodes.Items {
		nodeState, err := p.EvaluateNode(ctx, &nodes.Items[i], management)
		if err != nil {
			return ProviderState{}, err
		}

		if management.OperatorVersionKnown && !management.ClientLabelManaged {
			if err := p.releaseClientLabelManagement(ctx, &nodes.Items[i]); err != nil {
				return ProviderState{}, err
			}
		} else if management.ClientLabelManaged {
			if strings.HasPrefix(nodes.Items[i].Labels[ClientDeployLabel], "paused-") {
				if err := p.releaseClientLabelManagement(ctx, &nodes.Items[i]); err != nil {
					return ProviderState{}, err
				}

				continue
			}

			if err := p.patchClientLabel(ctx, &nodes.Items[i], nodeState.DriverReady); err != nil {
				return ProviderState{}, err
			}
		}

		state.NodeStates[nodeState.Node] = nodeState
		if !nodeState.Ready {
			state.Ready = false
			if state.Reason == "" {
				state.Reason = nodeState.Reason
			}
		}

		if nodeState.ProviderStartKnown && nodeState.ProviderLastContainerStart.After(latest) {
			latest = nodeState.ProviderLastContainerStart
		}
	}

	if !latest.IsZero() {
		state.ProviderStartKnown = true
		state.ProviderLastContainerStart = latest
	}

	return state, nil
}

func (p *NvidiaProvider) releaseClientLabelManagement(ctx context.Context, node *corev1.Node) error {
	if node == nil || node.Annotations == nil {
		return nil
	}

	if _, managed := node.Annotations[ClientDeployManagedAnnotation]; !managed {
		return nil
	}

	base := node.DeepCopy()
	delete(node.Annotations, ClientDeployManagedAnnotation)

	if err := p.Client.Patch(ctx, node, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("release client label management for node %s: %w", node.Name, err)
	}

	return nil
}

func (p *NvidiaProvider) patchClientLabel(ctx context.Context, node *corev1.Node, ready bool) error {
	if node == nil {
		return nil
	}

	desired := "false"
	if ready {
		desired = NVIDIAGPUNodeValue
	}

	_, managed := node.Annotations[ClientDeployManagedAnnotation]
	_, labelExists := node.Labels[ClientDeployLabel]

	if !managed && labelExists {
		return nil
	}

	base := node.DeepCopy()

	if node.Labels == nil {
		node.Labels = map[string]string{}
	}

	node.Labels[ClientDeployLabel] = desired
	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}

	node.Annotations[ClientDeployManagedAnnotation] = NVIDIAGPUNodeValue

	if managed && base.Labels[ClientDeployLabel] == desired && base.Annotations[ClientDeployManagedAnnotation] == NVIDIAGPUNodeValue {
		return nil
	}

	if err := p.Client.Patch(ctx, node, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("patch node %s client deployment label and management annotation: %w", node.Name, err)
	}

	return nil
}

func (p *NvidiaProvider) EvaluateNode(ctx context.Context, node *corev1.Node, management ProviderManagement) (ProviderNodeState, error) {
	result := nvidiaNodeState{}
	if node != nil {
		result.Node = node.Name
	}

	if !management.Managed {
		result.Ready = true
		result.Reason = "GPU Operator provider is not managed"

		return result, nil
	}

	if node == nil || node.Labels[NVIDIAGPUNodeLabel] != NVIDIAGPUNodeValue {
		result.Ready = true
		result.Reason = "node is not an NVIDIA GPU node"

		return result, nil
	}

	driver, err := p.providerPods(ctx, node, NVIDIADriverDeployLabel)
	if err != nil {
		return result, err
	}

	toolkit, err := p.providerPods(ctx, node, NVIDIAToolkitDeployLabel)
	if err != nil {
		return result, err
	}

	if management.DriverManaged && len(driver) == 0 {
		result.Reason = "NVIDIA driver provider is not Ready"
		return result, nil
	}

	result.DriverReady = !management.DriverManaged || len(driver) > 0
	if management.ToolkitManaged && len(toolkit) == 0 {
		result.Reason = "NVIDIA container toolkit provider is not Ready"
		return result, nil
	}

	for _, provider := range append(driver, toolkit...) {
		if !podReady(&provider.Pod) {
			result.Reason = "NVIDIA provider Pod is Running but not Ready"
			return result, nil
		}
	}

	if duplicateProvider(driver) || duplicateProvider(toolkit) {
		result.Reason = "NVIDIA provider rollout has duplicate current Pods"
		return result, nil
	}

	providers := make([]*corev1.Pod, 0, len(driver)+len(toolkit))
	for i := range driver {
		providers = append(providers, &driver[i].Pod)
	}

	for i := range toolkit {
		providers = append(providers, &toolkit[i].Pod)
	}

	start, known := providerLastContainerStart(providers)
	if !known {
		result.Reason = "NVIDIA provider container start time is unknown"
		return result, nil
	}

	result.Ready = true
	result.ProviderLastContainerStart = start
	result.ProviderStartKnown = true

	return result, nil
}

func (p *NvidiaProvider) providerPods(ctx context.Context, node *corev1.Node, deploymentLabel string) ([]providerPod, error) {
	daemonSets := &appsv1.DaemonSetList{}
	if err := p.Client.List(ctx, daemonSets); err != nil {
		return nil, fmt.Errorf("list NVIDIA DaemonSets: %w", err)
	}

	pods := &corev1.PodList{}
	if err := p.Client.List(ctx, pods); err != nil {
		return nil, fmt.Errorf("list provider Pods on node %s: %w", node.Name, err)
	}

	result := make([]providerPod, 0)

	for i := range daemonSets.Items {
		daemonSet := &daemonSets.Items[i]
		if daemonSet.Spec.Template.Spec.NodeSelector[deploymentLabel] != NVIDIAGPUNodeValue || !matchesSelector(node, daemonSet.Spec.Template.Spec.NodeSelector) {
			continue
		}

		revision, err := p.currentRevision(ctx, daemonSet)
		if err != nil {
			return nil, err
		}

		for j := range pods.Items {
			pod := &pods.Items[j]
			if pod.Spec.NodeName != node.Name || !ownedByDaemonSet(pod, daemonSet) || !podCurrentRunning(pod, daemonSet, revision) {
				continue
			}

			result = append(result, providerPod{Pod: *pod, DaemonSet: *daemonSet, Revision: revision})
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].DaemonSet.Namespace+"/"+result[i].DaemonSet.Name < result[j].DaemonSet.Namespace+"/"+result[j].DaemonSet.Name
	})

	return result, nil
}

func (p *NvidiaProvider) currentRevision(ctx context.Context, daemonSet *appsv1.DaemonSet) (string, error) {
	revisions := &appsv1.ControllerRevisionList{}
	if err := p.Client.List(ctx, revisions, client.InNamespace(daemonSet.Namespace)); err != nil {
		return "", fmt.Errorf("list revisions for DaemonSet %s/%s: %w", daemonSet.Namespace, daemonSet.Name, err)
	}
	var current *appsv1.ControllerRevision

	for i := range revisions.Items {
		revision := &revisions.Items[i]
		if !ownedByRevision(revision, daemonSet) || current != nil && revision.Revision <= current.Revision {
			continue
		}

		current = revision
	}

	if current == nil {
		return "", nil
	}

	if hash := current.Labels[controllerRevisionHashLabel]; hash != "" {
		return hash, nil
	}

	if hash := current.Labels[controllerRevisionHashAlt]; hash != "" {
		return hash, nil
	}

	if prefix := daemonSet.Name + "-"; strings.HasPrefix(current.Name, prefix) {
		return strings.TrimPrefix(current.Name, prefix), nil
	}

	return current.Name, nil
}

func providerLastContainerStart(pods []*corev1.Pod) (time.Time, bool) {
	if len(pods) == 0 {
		return time.Time{}, false
	}
	var latest time.Time

	for _, pod := range pods {
		if pod == nil || len(pod.Status.ContainerStatuses) == 0 {
			return time.Time{}, false
		}

		for _, status := range pod.Status.ContainerStatuses {
			if status.State.Running == nil || status.State.Running.StartedAt.IsZero() {
				return time.Time{}, false
			}

			if status.State.Running.StartedAt.Time.After(latest) {
				latest = status.State.Running.StartedAt.Time
			}
		}
	}

	return latest, !latest.IsZero()
}

func duplicateProvider(providers []providerPod) bool {
	seen := make(map[string]struct{}, len(providers))

	for _, provider := range providers {
		key := provider.DaemonSet.Namespace + "/" + provider.DaemonSet.Name
		if _, ok := seen[key]; ok {
			return true
		}

		seen[key] = struct{}{}
	}

	return false
}

func matchesSelector(node *corev1.Node, selector map[string]string) bool {
	if node == nil {
		return false
	}

	for key, value := range selector {
		if node.Labels[key] != value {
			return false
		}
	}

	return true
}

func ownedByDaemonSet(pod *corev1.Pod, daemonSet *appsv1.DaemonSet) bool {
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == daemonSetKind && owner.Name == daemonSet.Name && owner.UID == daemonSet.UID {
			return true
		}
	}

	return false
}

func ownedByRevision(revision *appsv1.ControllerRevision, daemonSet *appsv1.DaemonSet) bool {
	for _, owner := range revision.OwnerReferences {
		if owner.Kind == daemonSetKind && owner.Name == daemonSet.Name && owner.UID == daemonSet.UID {
			return true
		}
	}

	return false
}

func podCurrentRunning(pod *corev1.Pod, daemonSet *appsv1.DaemonSet, revision string) bool {
	if pod == nil || daemonSet == nil || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || revision == "" || !ownedByDaemonSet(pod, daemonSet) {
		return false
	}

	if pod.Labels[controllerRevisionHashLabel] != revision && pod.Labels[controllerRevisionHashAlt] != revision {
		return false
	}

	return true
}

func podReady(pod *corev1.Pod) bool {
	if pod == nil || pod.Status.Phase != corev1.PodRunning {
		return false
	}

	ready := false

	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			ready = true
			break
		}
	}

	if !ready || len(pod.Status.ContainerStatuses) == 0 {
		return false
	}

	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Running == nil {
			return false
		}
	}

	return true
}
