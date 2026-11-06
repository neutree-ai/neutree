package manifests

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clusterversion "github.com/neutree-ai/neutree/internal/cluster/version"
	"github.com/neutree-ai/neutree/pkg/accelerator"
)

const (
	NodeAgentName        = "neutree-node-agent"
	ExporterName         = "nvidia-gpu-dcgm-exporter"
	ExporterPort         = 19400
	NodeAgentPort        = 19101
	DefaultExporterImage = "nvcr.io/nvidia/k8s/dcgm-exporter:4.5.3-4.8.2-distroless"
	OwnerLabel           = "neutree.ai/accelerator-operator"
	OwnerValue           = "nvidia"
	DeploymentModeLabel  = "neutree.ai/accelerator-deployment-mode"
	DeploymentModeValue  = "operator"
	MetricsTargetLabel   = "neutree.ai/metrics-target"
	AcceleratorTypeLabel = "neutree.ai/accelerator-type"
	ClientDeployLabel    = "nvidia.com/gpu.deploy.client"
	ToolkitReadyHostPath = "/run/nvidia/validations"
	ToolkitReadyImage    = "busybox:1.36"
)

type NvidiaRenderOptions struct {
	Cluster         *v1.Cluster
	Namespace       string
	ImagePrefix     string
	ImagePullSecret string
	Profile         *v1.AcceleratorProfile
	Managed         bool
	Affinity        *corev1.Affinity
}

func RenderNvidia(ctx context.Context, options NvidiaRenderOptions) (*unstructured.UnstructuredList, error) {
	_ = ctx

	if options.Cluster == nil || options.Cluster.Metadata == nil {
		return nil, fmt.Errorf("cluster metadata is required")
	}

	if options.Profile == nil {
		return nil, fmt.Errorf("NVIDIA accelerator profile is required")
	}

	objects := []unstructured.Unstructured{}

	nodeAgent, err := renderNodeAgent(options)
	if err != nil {
		return nil, err
	}

	objects = append(objects, nodeAgent...)

	exporter, err := renderExporter(options)
	if err != nil {
		return nil, err
	}

	objects = append(objects, exporter...)

	return &unstructured.UnstructuredList{Items: objects}, nil
}

func renderNodeAgent(options NvidiaRenderOptions) ([]unstructured.Unstructured, error) {
	selection, err := clusterversion.SelectNodeAgent(options.Cluster.GetVersion())
	if err != nil {
		return nil, err
	}

	labels := objectLabels(options.Cluster, NodeAgentName, "node-agent", "")
	podLabels := cloneMap(labels)
	selector := map[string]string{"app": NodeAgentName, "cluster": options.Cluster.Metadata.Name, "workspace": options.Cluster.Metadata.Workspace}

	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: NodeAgentName, Namespace: options.Namespace, Labels: labels}}
	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: NodeAgentName, Labels: labels},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"get", "list", "watch", "patch"}},
			{APIGroups: []string{""}, Resources: []string{"nodes/proxy"}, Verbs: []string{"get"}},
			{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch", "patch"}},
		},
	}
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: NodeAgentName, Labels: labels},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: NodeAgentName, Namespace: options.Namespace}},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: NodeAgentName, APIGroup: "rbac.authorization.k8s.io"},
	}

	env := make([]corev1.EnvVar, 0, len(options.Profile.NodeAgentRuntime.Env)+2)
	for key, value := range options.Profile.NodeAgentRuntime.Env {
		env = append(env, corev1.EnvVar{Name: key, Value: value})
	}

	env = append(env,
		corev1.EnvVar{Name: "NODE_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}}},
		corev1.EnvVar{Name: "NODE_IP", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.hostIP"}}},
	)

	args := []string{"--listen-address=:" + strconv.Itoa(NodeAgentPort), "--cluster-type=" + v1.KubernetesClusterType, "--node=$(NODE_NAME)", "--node-ip=$(NODE_IP)"}
	args = append(args,
		"--accelerator-type="+v1.AcceleratorTypeNVIDIAGPU.String(),
		"--accelerator-exporter-port="+strconv.Itoa(ExporterPort),
		"--accelerator-exporter-metrics-path=/metrics",
	)

	nodeAgent := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: NodeAgentName, Namespace: options.Namespace, Labels: labels},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: selector},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
				Spec: corev1.PodSpec{
					Affinity:           options.Affinity,
					ServiceAccountName: NodeAgentName,
					ImagePullSecrets:   []corev1.LocalObjectReference{{Name: options.ImagePullSecret}},
					InitContainers:     toolkitReadyInitContainers(options.Managed),
					Volumes:            toolkitReadyVolumes(options.Managed),
					Containers: []corev1.Container{{
						Name: "neutree-node-agent", Image: rewriteImage(options.ImagePrefix, selection.Image), Args: args, Env: env,
						Ports: []corev1.ContainerPort{{Name: "metrics", ContainerPort: NodeAgentPort}},
						LivenessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
								Path: "/health", Port: intstrFromInt(NodeAgentPort),
							}},
							InitialDelaySeconds: 10, PeriodSeconds: 10,
						},
						SecurityContext: nodeAgentSecurityContext(options.Profile.NodeAgentRuntime),
					}},
				},
			},
		},
	}

	return toUnstructured(sa, role, binding, nodeAgent)
}

func renderExporter(options NvidiaRenderOptions) ([]unstructured.Unstructured, error) {
	profile := options.Profile.MetricsExporter
	if profile == nil {
		return nil, nil
	}

	labels := objectLabels(options.Cluster, profile.Name, "accelerator-exporter", v1.AcceleratorTypeNVIDIAGPU.String())
	podLabels := cloneMap(labels)
	selector := map[string]string{"app": profile.Name, "cluster": options.Cluster.Metadata.Name, "workspace": options.Cluster.Metadata.Workspace}

	image := profile.Image
	if image == "" {
		image = DefaultExporterImage
	}

	container := corev1.Container{
		Name: profile.Name, Image: rewriteImage(options.ImagePrefix, image),
		Command: append([]string{}, profile.Command...), Args: append([]string{}, profile.Args...),
		Env:   envVars(profile.Env),
		Ports: []corev1.ContainerPort{{Name: "metrics", ContainerPort: int32(profile.Port)}},
	}
	if container.Ports[0].ContainerPort == 0 {
		container.Ports[0].ContainerPort = ExporterPort
	}

	if profile.Runtime != nil {
		container.SecurityContext = exporterSecurityContext(profile.Runtime)
	}

	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: profile.Name, Namespace: options.Namespace, Labels: labels},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: selector},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
				Spec: corev1.PodSpec{
					Tolerations:      []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
					ImagePullSecrets: []corev1.LocalObjectReference{{Name: options.ImagePullSecret}},
					InitContainers:   toolkitReadyInitContainers(options.Managed),
					Volumes:          toolkitReadyVolumes(options.Managed),
					Containers:       []corev1.Container{container},
				},
			},
		},
	}

	return toUnstructured(ds)
}

func objectLabels(cluster *v1.Cluster, app, target, acceleratorType string) map[string]string {
	labels := map[string]string{
		OwnerLabel: OwnerValue, DeploymentModeLabel: DeploymentModeValue,
		"app": app, MetricsTargetLabel: target,
		"cluster": cluster.Metadata.Name, "workspace": cluster.Metadata.Workspace,
	}
	if acceleratorType != "" {
		labels[AcceleratorTypeLabel] = acceleratorType
	}

	return labels
}

func cloneMap(values map[string]string) map[string]string {
	result := map[string]string{}
	for key, value := range values {
		result[key] = value
	}

	return result
}

func rewriteImage(prefix, image string) string {
	if prefix == "" {
		return image
	}

	return strings.TrimRight(prefix, "/") + "/" + strings.TrimLeft(image, "/")
}

func envVars(values map[string]string) []corev1.EnvVar {
	result := make([]corev1.EnvVar, 0, len(values))
	for key, value := range values {
		result = append(result, corev1.EnvVar{Name: key, Value: value})
	}

	return result
}

func nodeAgentSecurityContext(runtime *accelerator.NodeAgentRuntime) *corev1.SecurityContext {
	if runtime == nil {
		return nil
	}

	return &corev1.SecurityContext{Privileged: &runtime.Privileged, Capabilities: runtime.Capabilities}
}

func exporterSecurityContext(runtime *accelerator.AcceleratorExporterRuntimeProfile) *corev1.SecurityContext {
	if runtime == nil {
		return nil
	}

	return &corev1.SecurityContext{Privileged: &runtime.Privileged, Capabilities: runtime.Capabilities}
}

func toolkitReadyInitContainers(enabled bool) []corev1.Container {
	if !enabled {
		return nil
	}

	mountPropagation := corev1.MountPropagationHostToContainer

	return []corev1.Container{{
		Name: "toolkit-validation", Image: ToolkitReadyImage,
		Command: []string{"sh", "-c"},
		Args:    []string{"until [ -f /run/nvidia/validations/toolkit-ready ] && { grep -q '^nvidia ' /proc/modules || [ -e /dev/dxg ]; }; do sleep 5; done"},
		VolumeMounts: []corev1.VolumeMount{{
			Name: "nvidia-validations", MountPath: ToolkitReadyHostPath,
			ReadOnly: true, MountPropagation: &mountPropagation,
		}},
	}}
}

func toolkitReadyVolumes(enabled bool) []corev1.Volume {
	if !enabled {
		return nil
	}

	hostPathType := corev1.HostPathDirectoryOrCreate

	return []corev1.Volume{{
		Name: "nvidia-validations",
		VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
			Path: ToolkitReadyHostPath, Type: &hostPathType,
		}},
	}}
}

func toUnstructured(objects ...interface{}) ([]unstructured.Unstructured, error) {
	result := make([]unstructured.Unstructured, 0, len(objects))

	for _, object := range objects {
		converted, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
		if err != nil {
			return nil, err
		}

		result = append(result, unstructured.Unstructured{Object: converted})
	}

	return result, nil
}

func intstrFromInt(value int) intstr.IntOrString {
	return intstr.FromInt(value)
}
