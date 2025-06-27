package accelerator

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
)

type Manager interface {
	Start(ctx context.Context)
	GetNodeAcceleratorType(ctx context.Context, nodeIp string, sshAuth v1.Auth) (string, error)
	GetKubernetesContainerAcceleratorType(ctx context.Context, container corev1.Container) (string, error)
	GetNodeRuntimeConfig(ctx context.Context, acceleratorType string, nodeIp string, sshAuth v1.Auth) (v1.RuntimeConfig, error)
	GetKubernetesContainerRuntimeConfig(ctx context.Context, acceleratorType string, container corev1.Container) (v1.RuntimeConfig, error)
	GetAllAcceleratorSupportEngines(ctx context.Context) ([]*v1.Engine, error)
}

type registerPlugin struct {
	resource         string
	plugin           plugin.AcceleratorPlugin
	lastRegisterTime time.Time
}

type manager struct {
	acceleratorsMap   sync.Map
	supportEnginesMap sync.Map
}

func NewManager(e *gin.Engine) Manager {
	manager := &manager{
		acceleratorsMap:   sync.Map{},
		supportEnginesMap: sync.Map{},
	}

	for _, p := range plugin.GetLocalAcceleratorPlugins() {
		manager.registerAcceleratorPlugin(p)
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

	acceleratorPlugin := plugin.NewAcceleratorRestPlugin(req.ResourceName, req.Endpoint)
	a.registerAcceleratorPlugin(acceleratorPlugin)

	c.JSON(http.StatusOK, "ok")
}

func (a *manager) registerAcceleratorPlugin(plugin plugin.AcceleratorPlugin) {
	// refresh engine cache when plugin register.
	// todo: we can determine whether an update is needed by checking the plugin version.
	a.refreshAcceleratorPluginSupportEngines(plugin)

	p := registerPlugin{
		resource:         plugin.Resource(),
		plugin:           plugin,
		lastRegisterTime: time.Now(),
	}

	previous, _ := a.acceleratorsMap.Swap(p.resource, p)
	if previous != nil {
		klog.Infof("Accelerator plugin %s already registered, update register time", p.resource)
	} else {
		klog.Infof("Register accelerator plugin: %s", p.resource)
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
	notifyEngineUpdate := make(chan struct{}, 1)

	go wait.UntilWithContext(ctx, func(ctx context.Context) {
		select {
		case <-ctx.Done():
			return
		default:
		}

		removePlugins := a.syncPlugins()

		if len(removePlugins) > 0 {
			notifyEngineUpdate <- struct{}{}
		}
	}, time.Minute)

	go func() {
		// refresh engine cache in first start.
		a.refreshAllAcceleratorPluginSupportEngines()

		for {
			select {
			case <-notifyEngineUpdate:
				a.refreshAllAcceleratorPluginSupportEngines()
			case <-ctx.Done():
				return
			}
		}
	}()
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

func (a *manager) GetKubernetesContainerAcceleratorType(ctx context.Context, container corev1.Container) (string, error) {
	var (
		err                  error
		pluginResource       string
		resultPluginResource string
		acceleratorResp      *v1.GetContainerAcceleratorResponse
	)

	a.acceleratorsMap.Range(func(key, value any) bool {
		p, ok := value.(registerPlugin)
		if !ok {
			klog.Warning("assert register plugin type failed")
			return true
		}

		pluginResource = p.resource

		acceleratorResp, err = p.plugin.Handle().GetKubernetesContainerAccelerator(ctx, &v1.GetContainerAcceleratorRequest{
			Container: container,
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
		return "", errors.Wrapf(err, "get container accelerator from plugin %s failed", pluginResource)
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

func (a *manager) GetKubernetesContainerRuntimeConfig(ctx context.Context, acceleratorType string, container corev1.Container) (v1.RuntimeConfig, error) {
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

	runtimeConfigResp, err := p.plugin.Handle().GetKubernetesContainerRuntimeConfig(ctx, &v1.GetContainerRuntimeConfigRequest{
		Container: container,
	})
	if err != nil {
		return v1.RuntimeConfig{}, errors.Wrapf(err, "get container runtime config from plugin %s failed", p.plugin.Resource())
	}

	return runtimeConfigResp.RuntimeConfig, nil
}

func (a *manager) GetAllAcceleratorSupportEngines(ctx context.Context) ([]*v1.Engine, error) {
	var engines []*v1.Engine

	a.supportEnginesMap.Range(func(key, value any) bool {
		e, ok := value.(*v1.Engine)
		if !ok {
			klog.Warning("assert engine type failed")
			return true
		}

		engines = append(engines, e)

		return true
	})

	return engines, nil
}

func (a *manager) refreshAcceleratorPluginSupportEngines(p plugin.AcceleratorPlugin) {
	resp, err := p.Handle().GetSupportEngines(context.Background())
	if err != nil {
		klog.Warningf("get support engines from plugin %s failed, err: %s", p.Resource(), err.Error())
		return
	}

	for _, e := range resp.Engines {
		a.supportEnginesMap.Store(e.Metadata.Name, e)
	}
}

func (a *manager) refreshAllAcceleratorPluginSupportEngines() {
	// reset support engines map
	a.supportEnginesMap = sync.Map{}
	a.acceleratorsMap.Range(func(key, value any) bool {
		p, ok := value.(registerPlugin)
		if !ok {
			klog.Warning("assert register plugin type failed")
			return true
		}

		a.refreshAcceleratorPluginSupportEngines(p.plugin)

		return true
	})
}
