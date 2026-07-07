package kubernetes

import (
	"context"
	"net"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	podresourcesv1 "k8s.io/kubelet/pkg/apis/podresources/v1"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
)

const DefaultKubeletPodResourcesSocket = "/var/lib/kubelet/pod-resources/kubelet.sock"
const defaultTimeout = 5 * time.Second

type KubeletPodResourceLister struct {
	SocketPath string
	Timeout    time.Duration
}

func (l KubeletPodResourceLister) ListPodResources(ctx context.Context) ([]model.PodResource, error) {
	socketPath := l.SocketPath
	if socketPath == "" {
		socketPath = DefaultKubeletPodResourcesSocket
	}

	timeout := l.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := grpc.NewClient(
		"unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, target string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", strings.TrimPrefix(target, "unix://"))
		}),
	)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	client := podresourcesv1.NewPodResourcesListerClient(conn)

	response, err := client.List(dialCtx, &podresourcesv1.ListPodResourcesRequest{})
	if err != nil {
		return nil, err
	}

	return podResourcesFromKubelet(response.GetPodResources()), nil
}

func podResourcesFromKubelet(input []*podresourcesv1.PodResources) []model.PodResource {
	result := make([]model.PodResource, 0, len(input))

	for _, pod := range input {
		if pod == nil {
			continue
		}

		containers := make([]model.ContainerDevices, 0, len(pod.GetContainers()))

		for _, container := range pod.GetContainers() {
			if container == nil {
				continue
			}

			for _, device := range container.GetDevices() {
				if device == nil {
					continue
				}

				containers = append(containers, model.ContainerDevices{
					ResourceName: device.GetResourceName(),
					DeviceIDs:    append([]string{}, device.GetDeviceIds()...),
				})
			}
		}

		result = append(result, model.PodResource{
			Namespace:  pod.GetNamespace(),
			Name:       pod.GetName(),
			Containers: containers,
		})
	}

	return result
}
