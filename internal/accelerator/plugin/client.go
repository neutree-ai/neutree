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

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type acceleratorPluginClient struct {
	client  *http.Client
	baseURL string
}

// restResourceConverter is the REST resource converter
// Used by external plugins to perform resource conversion via HTTP API
type restResourceConverter struct {
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

func (u *acceleratorPluginClient) GetKubernetesContainerAccelerator(ctx context.Context,
	request *v1.GetContainerAcceleratorRequest) (*v1.GetContainerAcceleratorResponse, error) {
	response := &v1.GetContainerAcceleratorResponse{}

	err := u.doPost(ctx, v1.GetContainerAcceleratorPath, request, response)
	if err != nil {
		return nil, err
	}

	return response, nil
}

func (u *acceleratorPluginClient) GetKubernetesContainerRuntimeConfig(ctx context.Context,
	request *v1.GetContainerRuntimeConfigRequest) (*v1.GetContainerRuntimeConfigResponse, error) {
	response := &v1.GetContainerRuntimeConfigResponse{}

	err := u.doPost(ctx, v1.GetContainerRuntimeConfigPath, request, response)
	if err != nil {
		return nil, err
	}

	return response, nil
}

func (u *acceleratorPluginClient) GetResourceConverter() v1.ResourceConverter {
	return &restResourceConverter{
		client:  u.client,
		baseURL: u.baseURL,
	}
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
func (r *restResourceConverter) ConvertToRay(spec *v1.ResourceSpec) (*v1.RayResourceSpec, error) {
	reqContent, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, r.baseURL+v1.ConvertToRayPath, bytes.NewBuffer(reqContent))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	result := &v1.RayResourceSpec{}
	if err := parsePluginResponse(resp, result); err != nil {
		return nil, err
	}

	return result, nil
}

// ConvertToKubernetes converts to Kubernetes resource configuration via REST API
func (r *restResourceConverter) ConvertToKubernetes(spec *v1.ResourceSpec) (*v1.KubernetesResourceSpec, error) {
	reqContent, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, r.baseURL+v1.ConvertToKubernetesPath, bytes.NewBuffer(reqContent))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	result := &v1.KubernetesResourceSpec{}
	if err := parsePluginResponse(resp, result); err != nil {
		return nil, err
	}

	return result, nil
}
