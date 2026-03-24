package accelerator

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
)

type Manager interface {
	Start(ctx context.Context)
	GetNodeAcceleratorType(ctx context.Context, nodeIp string, sshAuth v1.Auth) (string, error)
	GetNodeRuntimeConfig(ctx context.Context, acceleratorType string, nodeIp string, sshAuth v1.Auth) (v1.RuntimeConfig, error)

	// GetAllConverters returns all registered resource converters
	GetAllConverters() map[string]plugin.ResourceConverter

	// GetAllParsers returns all registered resource parsers
	GetAllParsers() map[string]plugin.ResourceParser

	// GetConverter retrieves a resource converter by accelerator type
	GetConverter(acceleratorType string) (plugin.ResourceConverter, bool)

	// GetParser retrieves a resource parser by accelerator type
	GetParser(acceleratorType string) (plugin.ResourceParser, bool)

	// GetEngineContainerRunOptions returns Docker run_options for engine containers.
	// Delegates to the registered plugin's GetContainerRuntimeConfig() and converts
	// RuntimeConfig fields (Runtime, Options, Env) to Docker CLI flags.
	GetEngineContainerRunOptions(acceleratorType string) ([]string, error)

	// GetImageSuffix returns the image suffix for a given accelerator type
	// (e.g. "rocm" for amd_gpu). Returns empty string for default variant or if
	// the accelerator type is not registered.
	GetImageSuffix(acceleratorType string) string
}

type registerPlugin struct {
	resource         string
	plugin           plugin.AcceleratorPlugin
	lastRegisterTime time.Time
}

type manager struct {
	acceleratorsMap sync.Map
}

func NewManager(e *gin.Engine) *manager {
	manager := &manager{
		acceleratorsMap: sync.Map{},
	}

	for _, p := range plugin.GetLocalAcceleratorPlugins() {
		manager.acceleratorsMap.Store(p.Resource(), registerPlugin{
			resource:         p.Resource(),
			plugin:           p,
			lastRegisterTime: time.Now(),
		})

		klog.Infof("Register local accelerator plugin: %s", p.Resource())
	}

	// register plugin register handler
	pluginGroup := e.Group(v1.PluginAPIGroupPath)
	pluginGroup.POST("/register", manager.registerHandler)

	return manager
}

func (a *manager) registerHandler(c *gin.Context) {
	var req v1.RegisterRequest

	err := c.ShouldBindBodyWithJSON(&req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	a.registerAcceleratorPlugin(req)

	c.JSON(http.StatusOK, "ok")
}

func (a *manager) registerAcceleratorPlugin(req v1.RegisterRequest) {
	value, ok := a.acceleratorsMap.Load(req.ResourceName)
	if ok {
		klog.Infof("Accelerator plugin %s already registered, update register time", req.ResourceName)

		p, ok := value.(registerPlugin)
		if !ok {
			klog.Warning("assert register plugin type failed")
			return
		}

		updatedPlugin := registerPlugin{
			resource:         p.resource,
			plugin:           p.plugin,
			lastRegisterTime: time.Now(),
		}

		a.acceleratorsMap.Store(req.ResourceName, updatedPlugin)
	} else {
		p := registerPlugin{
			resource:         req.ResourceName,
			plugin:           plugin.NewAcceleratorRestPlugin(req.ResourceName, req.Endpoint),
			lastRegisterTime: time.Now(),
		}
		a.acceleratorsMap.Store(req.ResourceName, p)

		klog.Infof("Register accelerator plugin: %s", req.ResourceName)
	}
}

// syncPlugins sync all register plugin status and will remove unhealthy plugin.
// return removed plugin resource name list.
func (a *manager) syncPlugins() []string {
	needRemovedPlugins := a.getUnhealthyPlugins()

	for _, resource := range needRemovedPlugins {
		a.acceleratorsMap.Delete(resource)
	}

	if len(needRemovedPlugins) > 0 {
		klog.Infof("Accelerator plugin %s removed", needRemovedPlugins)
	}

	return needRemovedPlugins
}

func (a *manager) getUnhealthyPlugins() []string {
	var removedPlugins []string

	a.acceleratorsMap.Range(func(key, value any) bool {
		p, ok := value.(registerPlugin)
		if !ok {
			klog.Warning("assert register plugin type failed")
			return true
		}

		if p.plugin.Type() == plugin.InternalPluginType {
			return true
		}

		if time.Since(p.lastRegisterTime) > time.Minute*2 {
			if err := p.plugin.Handle().Ping(context.Background()); err != nil {
				klog.Warningf("Accelerator plugin %s ping failed, err: %s", p.resource, err.Error())
				removedPlugins = append(removedPlugins, p.resource)
			}
		}

		return true
	})

	return removedPlugins
}

func (a *manager) Start(ctx context.Context) {
	go wait.UntilWithContext(ctx, func(ctx context.Context) {
		select {
		case <-ctx.Done():
			return
		default:
		}

		a.syncPlugins()
	}, time.Minute)
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

func (a *manager) GetAllParsers() map[string]plugin.ResourceParser {
	result := make(map[string]plugin.ResourceParser)

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

func (a *manager) GetParser(acceleratorType string) (plugin.ResourceParser, bool) {
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
