package plugin

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type acceleratorPluginClient struct {
	client  *http.Client
	baseURL string
}

func newAcceleratorPluginClient(baseUrl string) AcceleratorPluginHandle {
	return &acceleratorPluginClient{
		client: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true, //nolint:gosec
				},
			},
			Timeout: time.Minute,
		},
		baseURL: baseUrl,
	}
}

func (u *acceleratorPluginClient) Ping(ctx context.Context) error {
	err := u.doGet(ctx, v1.PingPath, nil)
	if err != nil {
		return err
	}

	return nil
}

func (u *acceleratorPluginClient) GetSupportEngines(ctx context.Context) (*v1.GetSupportEnginesResponse, error) {
	response := &v1.GetSupportEnginesResponse{}

	err := u.doGet(ctx, v1.GetSupportEnginesPath, response)
	if err != nil {
		return nil, err
	}

	return response, nil
}

func (u *acceleratorPluginClient) GetNodeAccelerator(ctx context.Context,
	request *v1.GetNodeAcceleratorRequest) (*v1.GetNodeAcceleratorResponse, error) {
	response := &v1.GetNodeAcceleratorResponse{}

	err := u.doPost(ctx, v1.GetNodeAcceleratorPath, request, response)
	if err != nil {
		return nil, err
	}

	return response, nil
}

func (u *acceleratorPluginClient) GetNodeRuntimeConfig(ctx context.Context,
	request *v1.GetNodeRuntimeConfigRequest) (*v1.GetNodeRuntimeConfigResponse, error) {
	response := &v1.GetNodeRuntimeConfigResponse{}

	err := u.doPost(ctx, v1.GetNodeRuntimeConfigPath, request, response)
	if err != nil {
		return nil, err
	}

	return response, nil
}

func (u *acceleratorPluginClient) GetResourceConverter() ResourceConverter {
	return u
}

func (u *acceleratorPluginClient) GetResourceParser() ResourceParser {
	return u
}

func (u *acceleratorPluginClient) doPost(ctx context.Context, path string, request, response interface{}) error {
	reqContent, err := json.Marshal(request)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.baseURL+path, bytes.NewBuffer(reqContent))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := u.client.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	return parsePluginResponse(resp, response)
}

func (u *acceleratorPluginClient) doGet(ctx context.Context, path string, response interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.baseURL+path, nil)
	if err != nil {
		return err
	}

	resp, err := u.client.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	if response == nil {
		return nil
	}

	return parsePluginResponse(resp, response)
}

func parsePluginResponse(resp *http.Response, result interface{}) error {
	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("get node accelerator failed, status code: %d, content: %s", resp.StatusCode, string(content))
	}

	err = json.Unmarshal(content, result)
	if err != nil {
		return err
	}

	return nil
}

// ConvertToRay converts to Ray resource configuration via REST API
func (u *acceleratorPluginClient) ConvertToRay(spec *v1.ResourceSpec) (*v1.RayResourceSpec, error) {
	response := &v1.RayResourceSpec{}

	err := u.doPost(context.Background(), v1.ConvertToRayPath, spec, response)
	if err != nil {
		return nil, err
	}

	return response, nil
}

// ConvertToKubernetes converts to Kubernetes resource configuration via REST API
func (u *acceleratorPluginClient) ConvertToKubernetes(spec *v1.ResourceSpec) (*v1.KubernetesResourceSpec, error) {
	resp := &v1.KubernetesResourceSpec{}
	if err := u.doPost(context.Background(), v1.ConvertToKubernetesPath, spec, resp); err != nil {
		return nil, err
	}

	return resp, nil
}

// ParseFromKubernetes parses resource info from Kubernetes resource quantities via REST API
func (u *acceleratorPluginClient) ParseFromKubernetes(resource map[corev1.ResourceName]resource.Quantity, labels map[string]string) (*v1.ResourceInfo, error) {
	resp := &v1.ResourceInfo{}
	if err := u.doPost(context.Background(), v1.ParseFromKubernetesPath, &v1.ParseFromKubernetesRequest{
		Resource: resource,
		Labels:   labels,
	}, resp); err != nil {
		return nil, err
	}

	return resp, nil
}

// ParseFromRay parses resource info from Ray resource configuration via REST API
func (u *acceleratorPluginClient) ParseFromRay(resource map[string]float64) (*v1.ResourceInfo, error) {
	resp := &v1.ResourceInfo{}
	if err := u.doPost(context.Background(), v1.ParseFromRayPath, &v1.ParseFromRayRequest{
		Resource: resource,
	}, resp); err != nil {
		return nil, err
	}

	return resp, nil
}
