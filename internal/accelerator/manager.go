package accelerator

import (
	"context"
	"sync"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
)

type registerPlugin struct {
	resource        string
	plugin          plugin.AcceleratorPlugin
	lastRegsterTime time.Time
}

type Manager struct {
	accelerators   map[string]registerPlugin
	supportEngines map[string]*v1.Engine
	engineRlock    sync.RWMutex
	rlock          sync.RWMutex
	server         plugin.PluginServer
}

func NewManager(listenAddr string) *Manager {
	manager := &Manager{
		accelerators:   make(map[string]registerPlugin),
		supportEngines: make(map[string]*v1.Engine),
	}

	for _, p := range plugin.GetLocalAcceleratorPlugins() {
		manager.registerAcceleratorPlugin(p)
	}

	manager.server = plugin.NewPluginServer(listenAddr, manager.registerAcceleratorPlugin)

	return manager
}

func (a *Manager) registerAcceleratorPlugin(plugin plugin.AcceleratorPlugin) {
	a.rlock.Lock()
	defer a.rlock.Unlock()

	// refresh engine cache when plugin register.
	// todo: we can determine whether an update is needed by checking the plugin version.
	a.refreshAcceleratorPluginSupportEngines(plugin)

	p := registerPlugin{
		resource:        plugin.Resource(),
		plugin:          plugin,
		lastRegsterTime: time.Now(),
	}

	_, ok := a.accelerators[plugin.Resource()]
	if ok {
		klog.Infof("Accelerator plugin %s already registered, update register time", plugin.Resource())
	} else {
		klog.Infof("Register accelerator plugin: %s", plugin.Resource())
	}

	a.accelerators[plugin.Resource()] = p
}

// syncPlugins sync all register plugin status and will remove unhealthy plugin.
// return removed plugin resource name list.
func (a *Manager) syncPlugins() []string {
	a.rlock.Lock()
	defer a.rlock.Unlock()

	removedPlugins := make([]string, 0)

	for _, p := range a.accelerators {
		// skip to check internal plugin.
		if p.plugin.Type() == plugin.InternalPluginType {
			continue
		}

		if time.Since(p.lastRegsterTime) > time.Minute*2 {
			if err := p.plugin.Handle().Ping(context.Background()); err != nil {
				klog.Warningf("Accelerator plugin %s ping failed, err: %s", p.resource, err.Error())
				delete(a.accelerators, p.resource)
				removedPlugins = append(removedPlugins, p.resource)
			}
		}
	}

	if len(removedPlugins) != 0 {
		klog.Infof("Accelerator plugin %s removed", removedPlugins)
	}

	return removedPlugins
}

func (a *Manager) Start(ctx context.Context) {
	a.server.Start(ctx)

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

func (a *Manager) GetNodeAcceleratorType(ctx context.Context, nodeIp string, sshAuth v1.Auth) (string, error) {
	a.rlock.RLock()
	defer a.rlock.RUnlock()

	for _, p := range a.accelerators {
		acceleratorResp, err := p.plugin.Handle().GetNodeAccelerator(ctx, &v1.GetNodeAcceleratorRequest{
			NodeIp:  nodeIp,
			SSHAuth: sshAuth,
		})

		if err != nil {
			klog.V(4).ErrorS(err, "get node accelerator from plugin %s failed, err: %s", p.plugin.Resource())
			continue
		}

		// by default, nodes will only mount accelerator cards from the same manufacturer.
		if len(acceleratorResp.Accelerators) > 0 {
			return p.plugin.Resource(), nil
		}
	}

	return "", nil
}

func (a *Manager) GetKubernetesContainerAcceleratorType(ctx context.Context, container corev1.Container) (string, error) {
	a.rlock.RLock()
	defer a.rlock.RUnlock()

	for _, p := range a.accelerators {
		acceleratorResp, err := p.plugin.Handle().GetKubernetesContainerAccelerator(ctx, &v1.GetContainerAcceleratorRequest{
			Container: container,
		})

		if err != nil {
			klog.V(4).ErrorS(err, "get container accelerator from plugin %s failed, err: %s", p.plugin.Resource())
			continue
		}

		// by default, container will only config accelerator resource from the same manufacturer.
		if len(acceleratorResp.Accelerators) > 0 {
			return p.plugin.Resource(), nil
		}
	}

	return "", nil
}

func (a *Manager) GetNodeRuntimeConfig(ctx context.Context, acceleratorType string, nodeIp string, sshAuth v1.Auth) (v1.RuntimeConfig, error) {
	if acceleratorType == "" {
		return v1.RuntimeConfig{}, nil
	}

	a.rlock.RLock()
	defer a.rlock.RUnlock()

	p, ok := a.accelerators[acceleratorType]
	if !ok {
		return v1.RuntimeConfig{}, errors.Errorf("accelerator plugin %s not found", acceleratorType)
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

func (a *Manager) GetKubernetesContainerRuntimeConfig(ctx context.Context, acceleratorType string, container corev1.Container) (v1.RuntimeConfig, error) {
	if acceleratorType == "" {
		return v1.RuntimeConfig{}, nil
	}

	a.rlock.RLock()
	defer a.rlock.RUnlock()

	p, ok := a.accelerators[acceleratorType]
	if !ok {
		return v1.RuntimeConfig{}, errors.Errorf("accelerator plugin %s not found", acceleratorType)
	}

	runtimeConfigResp, err := p.plugin.Handle().GetKubernetesContainerRuntimeConfig(ctx, &v1.GetContainerRuntimeConfigRequest{
		Container: container,
	})
	if err != nil {
		return v1.RuntimeConfig{}, errors.Wrapf(err, "get container runtime config from plugin %s failed", p.plugin.Resource())
	}

	return runtimeConfigResp.RuntimeConfig, nil
}

func (a *Manager) GetAllAcceleratorSupportEngines(ctx context.Context) ([]*v1.Engine, error) {
	a.engineRlock.RLock()
	defer a.engineRlock.RUnlock()
	var engines []*v1.Engine

	for _, e := range a.supportEngines {
		engines = append(engines, e)
	}

	return engines, nil
}

func (a *Manager) refreshAcceleratorPluginSupportEngines(p plugin.AcceleratorPlugin) {
	a.engineRlock.Lock()
	defer a.engineRlock.Unlock()

	resp, err := p.Handle().GetSupportEngines(context.Background())
	if err != nil {
		klog.Warningf("get support engines from plugin %s failed, err: %s", p.Resource(), err.Error())
		return
	}

	for _, e := range resp.Engines {
		a.supportEngines[e.Metadata.Name] = e
	}
}

func (a *Manager) refreshAllAcceleratorPluginSupportEngines() {
	a.engineRlock.Lock()
	defer a.engineRlock.Unlock()

	a.supportEngines = make(map[string]*v1.Engine)
	for _, p := range a.accelerators {
		resp, err := p.plugin.Handle().GetSupportEngines(context.Background())
		if err != nil {
			klog.Warningf("get support engines from plugin %s failed, err: %s", p.resource, err.Error())
			continue
		}

		for _, e := range resp.Engines {
			a.supportEngines[e.Metadata.Name] = e
		}
	}
}
