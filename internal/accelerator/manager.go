package accelerator

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	publicaccelerator "github.com/neutree-ai/neutree/pkg/accelerator"
)

type Manager interface {
	plugin.AcceleratorPluginProvider

	DetectAccelerator(ctx context.Context, nodeIP string, sshAuth v1.Auth) (*v1.StaticNodeAcceleratorStatus, error)
	GetAcceleratorProfile(ctx context.Context, acceleratorType string) (*v1.AcceleratorProfile, error)
	GetStaticNodeRuntimeConfig(ctx context.Context, accelerator *v1.StaticNodeAcceleratorStatus) (*v1.RuntimeConfig, error)
	GetNodeAcceleratorType(ctx context.Context, nodeIp string, sshAuth v1.Auth) (string, error)
	GetNodeRuntimeConfig(ctx context.Context, acceleratorType string, nodeIp string, sshAuth v1.Auth) (v1.RuntimeConfig, error)

	// GetAllConverters returns all registered resource converters
	GetAllConverters() map[string]plugin.ResourceConverter

	// GetAllParsers returns all registered resource parsers
	GetAllParsers() map[string]resourceparser.ResourceParser

	// GetConverter retrieves a resource converter by accelerator type
	GetConverter(acceleratorType string) (plugin.ResourceConverter, bool)

	// GetParser retrieves a resource parser by accelerator type
	GetParser(acceleratorType string) (resourceparser.ResourceParser, bool)

	// GetEngineContainerRunOptions returns Docker run_options for engine containers.
	// Delegates to the registered plugin's GetContainerRuntimeConfig() and converts
	// RuntimeConfig fields (Runtime, Options, Env) to Docker CLI flags.
	GetEngineContainerRunOptions(acceleratorType string) ([]string, error)

	// GetImageSuffix returns the image suffix for a given accelerator type
	// (e.g. "rocm" for amd_gpu). Returns empty string for default variant or if
	// the accelerator type is not registered.
	GetImageSuffix(acceleratorType string) string

	// AddInternalPlugins registers internal accelerator plugins on an existing
	// manager without re-registering manager-owned routes. All plugins must be
	// internal type: the health sync loop removes any plugin whose Type() is
	// not InternalPluginType. The batch is validated atomically.
	AddInternalPlugins(plugins ...publicaccelerator.Plugin) error
}

type registerPlugin struct {
	resource string
	plugin   plugin.AcceleratorPlugin
}

type manager struct {
	acceleratorsMap sync.Map
}

func NewManager() *manager {
	manager, err := NewManagerWithPlugins()
	if err != nil {
		panic(err)
	}

	return manager
}

func NewManagerWithPlugins(injectedPlugins ...publicaccelerator.Plugin) (*manager, error) {
	manager := &manager{
		acceleratorsMap: sync.Map{},
	}

	for _, p := range plugin.GetLocalAcceleratorPlugins() {
		manager.acceleratorsMap.Store(p.Resource(), registerPlugin{
			resource: p.Resource(),
			plugin:   p,
		})

		klog.Infof("Register local accelerator plugin: %s", p.Resource())
	}

	if err := manager.AddInternalPlugins(injectedPlugins...); err != nil {
		return nil, err
	}

	return manager, nil
}

// AddInternalPlugins registers internal accelerator plugins on an existing
// manager. All plugins are validated before any is stored, so an invalid
// input fails atomically without leaving partially registered plugins.
// Validation is per invocation: concurrent calls are not serialized.
func (a *manager) AddInternalPlugins(plugins ...publicaccelerator.Plugin) error {
	seen := make(map[string]struct{}, len(plugins))

	for _, p := range plugins {
		if isNilPlugin(p) {
			return fmt.Errorf("accelerator plugin is nil")
		}

		if p.Resource() == "" {
			return fmt.Errorf("accelerator plugin resource is required")
		}

		if _, exists := seen[p.Resource()]; exists {
			return fmt.Errorf("accelerator plugin resource %q is already registered", p.Resource())
		}

		if _, exists := a.acceleratorsMap.Load(p.Resource()); exists {
			return fmt.Errorf("accelerator plugin resource %q is already registered", p.Resource())
		}

		seen[p.Resource()] = struct{}{}
	}

	for _, p := range plugins {
		a.acceleratorsMap.Store(p.Resource(), registerPlugin{
			resource: p.Resource(),
			plugin:   p,
		})
		klog.Infof("Register internal accelerator plugin: %s", p.Resource())
	}

	return nil
}

func isNilPlugin(p publicaccelerator.Plugin) bool {
	if p == nil {
		return true
	}

	value := reflect.ValueOf(p)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (a *manager) DetectAccelerator(
	ctx context.Context,
	nodeIP string,
	sshAuth v1.Auth,
) (*v1.StaticNodeAcceleratorStatus, error) {
	if nodeIP == "" {
		return nil, errors.New("node ip is required")
	}

	if sshAuth.SSHUser == "" || sshAuth.SSHPrivateKey == "" {
		return nil, errors.New("node ssh auth is required")
	}

	var detected *v1.StaticNodeAcceleratorStatus
	var detectErr error

	a.acceleratorsMap.Range(func(key, value any) bool {
		p, ok := value.(registerPlugin)
		if !ok {
			klog.Warning("assert register plugin type failed")
			return true
		}

		staticResponse, staticErr := p.plugin.Handle().DetectStaticNodeAccelerator(ctx, &v1.DetectStaticNodeAcceleratorRequest{
			NodeIp:  nodeIP,
			SSHAuth: sshAuth,
		})
		if staticErr == nil && staticResponse != nil && staticResponse.Matched && staticResponse.Accelerator != nil {
			detected = staticResponse.Accelerator

			return false
		}

		if staticErr != nil {
			klog.Warningf("detect static node accelerator from plugin %s failed: %s", p.resource, staticErr.Error())

			if detectErr == nil {
				detectErr = errors.Wrapf(staticErr, "detect static node accelerator from plugin %s failed", p.resource)
			}

			return true
		}

		return true
	})

	if detected != nil {
		return detected, nil
	}

	if detectErr != nil {
		return nil, detectErr
	}

	cpu := v1.CPUStaticNodeAcceleratorStatus()

	return &cpu, nil
}

func (a *manager) GetNodeAcceleratorType(ctx context.Context, nodeIp string, sshAuth v1.Auth) (string, error) {
	var (
		err                  error
		pluginResource       string
		resultPluginResource string
		acceleratorResp      *v1.GetNodeAcceleratorResponse
	)

	a.acceleratorsMap.Range(func(key, value any) bool {
		p, ok := value.(registerPlugin)
		if !ok {
			klog.Warning("assert register plugin type failed")
			return true
		}

		pluginResource = p.resource

		acceleratorResp, err = p.plugin.Handle().GetNodeAccelerator(ctx, &v1.GetNodeAcceleratorRequest{
			NodeIp:  nodeIp,
			SSHAuth: sshAuth,
		})

		if err != nil {
			return false
		}

		// by default, nodes will only mount accelerator cards from the same manufacturer.
		if len(acceleratorResp.Accelerators) > 0 {
			resultPluginResource = p.resource
			return false
		}

		return true
	})

	if err != nil {
		return "", errors.Wrapf(err, "get node accelerator from plugin %s failed", pluginResource)
	}

	return resultPluginResource, nil
}

func (a *manager) GetNodeRuntimeConfig(ctx context.Context, acceleratorType string, nodeIp string, sshAuth v1.Auth) (v1.RuntimeConfig, error) {
	resource := acceleratorType
	if resource == "" {
		return v1.RuntimeConfig{}, nil
	}

	value, ok := a.acceleratorsMap.Load(resource)
	if !ok {
		return v1.RuntimeConfig{}, errors.Errorf("accelerator plugin %s not found", acceleratorType)
	}

	p, ok := value.(registerPlugin)
	if !ok {
		return v1.RuntimeConfig{}, errors.New("assert register plugin type failed")
	}

	runtimeConfigResp, err := p.plugin.Handle().GetNodeRuntimeConfig(ctx, &v1.GetNodeRuntimeConfigRequest{
		NodeIp:  nodeIp,
		SSHAuth: sshAuth,
	})

	if err != nil {
		return v1.RuntimeConfig{}, errors.Wrapf(err, "get node runtime config from plugin %s failed", p.plugin.Resource())
	}

	return runtimeConfigResp.RuntimeConfig, nil
}

func (a *manager) GetAcceleratorProfile(
	ctx context.Context,
	acceleratorType string,
) (*v1.AcceleratorProfile, error) {
	if acceleratorType == "" {
		return nil, errors.New("accelerator type is required")
	}

	p, ok := a.GetPlugin(acceleratorType)
	if !ok {
		return nil, errors.Errorf("accelerator plugin %s not found", acceleratorType)
	}

	profile, err := p.Handle().GetAcceleratorProfile(ctx)
	if err != nil {
		return nil, errors.Wrapf(err, "get accelerator profile from plugin %s failed", p.Resource())
	}

	return profile, nil
}

func (a *manager) GetStaticNodeRuntimeConfig(
	ctx context.Context,
	acceleratorStatus *v1.StaticNodeAcceleratorStatus,
) (*v1.RuntimeConfig, error) {
	if acceleratorStatus == nil || acceleratorStatus.Type == "" {
		return nil, nil
	}

	owner, found := a.GetPlugin(acceleratorStatus.Type)
	if !found {
		return nil, errors.Errorf("accelerator plugin %s not found", acceleratorStatus.Type)
	}

	resolver, resolverOK := owner.Handle().(publicaccelerator.StaticNodeRuntimeConfigResolver)
	if !resolverOK {
		return nil, nil
	}

	config, matched, err := resolver.GetStaticNodeRuntimeConfig(ctx, acceleratorStatus)
	if err != nil {
		return nil, errors.Wrapf(
			err,
			"get static node runtime config for accelerator type %s from plugin %s",
			acceleratorStatus.Type,
			owner.Resource(),
		)
	}

	if matched && config == nil {
		return nil, errors.Errorf(
			"static runtime resolver for accelerator type %s from plugin %s returned nil config for a matched status",
			acceleratorStatus.Type,
			owner.Resource(),
		)
	}

	if !matched {
		return nil, errors.Errorf(
			"static runtime resolver for accelerator type %s from owner plugin %s did not match",
			acceleratorStatus.Type,
			owner.Resource(),
		)
	}

	return config, nil
}

func (a *manager) GetAllConverters() map[string]plugin.ResourceConverter {
	result := make(map[string]plugin.ResourceConverter)

	a.acceleratorsMap.Range(func(key, value any) bool {
		p, ok := value.(registerPlugin)
		if !ok {
			klog.Warning("assert register plugin type failed")
			return true
		}

		if converter := p.plugin.Handle().GetResourceConverter(); converter != nil {
			result[p.resource] = converter
		}

		return true
	})

	return result
}

func (a *manager) GetAllParsers() map[string]resourceparser.ResourceParser {
	result := make(map[string]resourceparser.ResourceParser)

	a.acceleratorsMap.Range(func(key, value any) bool {
		p, ok := value.(registerPlugin)
		if !ok {
			klog.Warning("assert register plugin type failed")
			return true
		}

		if parser := p.plugin.Handle().GetResourceParser(); parser != nil {
			result[p.resource] = parser
		}

		return true
	})

	return result
}

func (a *manager) SupportPlugins() []string {
	result := []string{}

	a.acceleratorsMap.Range(func(key, value any) bool {
		resource, ok := key.(string)
		if !ok {
			klog.Warning("assert registered plugin key type failed")
			return true
		}

		result = append(result, resource)

		return true
	})

	sort.Strings(result)

	return result
}

func (a *manager) GetPlugin(acceleratorType string) (plugin.AcceleratorPlugin, bool) {
	value, ok := a.acceleratorsMap.Load(acceleratorType)
	if !ok {
		return nil, false
	}

	p, ok := value.(registerPlugin)
	if !ok {
		klog.Warning("assert registered plugin type failed")
		return nil, false
	}

	return p.plugin, true
}

func (a *manager) GetConverter(acceleratorType string) (plugin.ResourceConverter, bool) {
	p, ok := a.GetPlugin(acceleratorType)
	if !ok {
		return nil, false
	}

	converter := p.Handle().GetResourceConverter()

	return converter, converter != nil
}

func (a *manager) GetParser(acceleratorType string) (resourceparser.ResourceParser, bool) {
	p, ok := a.GetPlugin(acceleratorType)
	if !ok {
		return nil, false
	}

	parser := p.Handle().GetResourceParser()

	return parser, parser != nil
}

func (a *manager) GetImageSuffix(acceleratorType string) string {
	if acceleratorType == "" {
		return ""
	}

	p, ok := a.GetPlugin(acceleratorType)
	if !ok {
		return ""
	}

	rc, err := p.Handle().GetContainerRuntimeConfig()
	if err != nil {
		return ""
	}

	return rc.ImageSuffix
}

func (a *manager) GetEngineContainerRunOptions(acceleratorType string) ([]string, error) {
	if acceleratorType == "" {
		return nil, nil
	}

	p, ok := a.GetPlugin(acceleratorType)
	if !ok {
		return nil, errors.Errorf("accelerator plugin %s not found", acceleratorType)
	}

	rc, err := p.Handle().GetContainerRuntimeConfig()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get container runtime config")
	}

	var opts []string

	if rc.Runtime != "" {
		opts = append(opts, "--runtime="+rc.Runtime)
	}

	opts = append(opts, rc.Options...)

	for k, v := range rc.Env {
		opts = append(opts, "-e", k+"="+v)
	}

	return opts, nil
}
