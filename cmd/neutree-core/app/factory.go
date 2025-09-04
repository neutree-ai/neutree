package app

import (
	"github.com/pkg/errors"

	"github.com/neutree-ai/neutree/cmd/neutree-core/app/config"
	"github.com/neutree-ai/neutree/controllers"
	"github.com/neutree-ai/neutree/pkg/scheme"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type ControllerOptions struct {
	config      *config.CoreConfig
	beforeHooks []controllers.HookFunc
	afterHooks  []controllers.HookFunc
	name        string

	obj     scheme.Object
	scheme  *scheme.Scheme
	storage storage.ObjectStorage
}

// ControllerFactory defines a function type for creating controllers
type ControllerFactory func(opts *ControllerOptions) (controllers.Reconciler, error)

func NewClusterControllerFactory() ControllerFactory {
	return func(opts *ControllerOptions) (controllers.Reconciler, error) {
		clusterController, err := controllers.NewClusterController(
			&controllers.ClusterControllerOption{
				Storage:                 opts.config.Storage,
				ImageService:            opts.config.ImageService,
				Gw:                      opts.config.Gateway,
				AcceleratorManager:      opts.config.AcceleratorManager,
				ObsCollectConfigManager: opts.config.ObsCollectConfigManager,
				MetricsRemoteWriteURL:   opts.config.ClusterControllerConfig.MetricsRemoteWriteURL,
				DefaultClusterVersion:   opts.config.ClusterControllerConfig.DefaultClusterVersion,
			},
		)

		if err != nil {
			return nil, errors.Wrapf(err, "failed to create cluster controller")
		}

		return clusterController, nil
	}
}

func NewEngineControllerFactory() ControllerFactory {
	return func(opts *ControllerOptions) (controllers.Reconciler, error) {
		engineController, err := controllers.NewEngineController(&controllers.EngineControllerOption{
			Storage: opts.config.Storage,
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create engine controller")
		}

		return engineController, nil
	}
}

func NewEndpointControllerFactory() ControllerFactory {
	return func(opts *ControllerOptions) (controllers.Reconciler, error) {
		endpointController, err := controllers.NewEndpointController(&controllers.EndpointControllerOption{
			Storage:            opts.config.Storage,
			ImageService:       opts.config.ImageService,
			Gw:                 opts.config.Gateway,
			AcceleratorManager: opts.config.AcceleratorManager,
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create endpoint controller")
		}

		return endpointController, nil
	}
}

func NewRoleControllerFactory() ControllerFactory {
	return func(opts *ControllerOptions) (controllers.Reconciler, error) {
		roleController, err := controllers.NewRoleController(&controllers.RoleControllerOption{
			Storage: opts.config.Storage,
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create role controller")
		}

		return roleController, nil
	}
}

func NewRoleAssignmentControllerFactory() ControllerFactory {
	return func(opts *ControllerOptions) (controllers.Reconciler, error) {
		roleAssignmentController, err := controllers.NewRoleAssignmentController(&controllers.RoleAssignmentControllerOption{
			Storage: opts.config.Storage,
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create role assignment controller")
		}

		return roleAssignmentController, nil
	}
}

func NewWorkspaceControllerFactory() ControllerFactory {
	return func(opts *ControllerOptions) (controllers.Reconciler, error) {
		workspaceController, err := controllers.NewWorkspaceController(&controllers.WorkspaceControllerOption{
			Storage:            opts.config.Storage,
			AcceleratorManager: opts.config.AcceleratorManager,
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create workspace controller")
		}

		return workspaceController, nil
	}
}

func NewApiKeyControllerFactory() ControllerFactory {
	return func(opts *ControllerOptions) (controllers.Reconciler, error) {
		apiKeyController, err := controllers.NewApiKeyController(&controllers.ApiKeyControllerOption{
			Storage: opts.config.Storage,
			Gw:      opts.config.Gateway,
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create api key controller")
		}

		return apiKeyController, nil
	}
}

func NewImageRegistryControllerFactory() ControllerFactory {
	return func(opts *ControllerOptions) (controllers.Reconciler, error) {
		imageRegistryController, err := controllers.NewImageRegistryController(&controllers.ImageRegistryControllerOption{
			Storage:      opts.config.Storage,
			ImageService: opts.config.ImageService,
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create image registry controller")
		}

		return imageRegistryController, nil
	}
}

func NewModelCatalogControllerFactory() ControllerFactory {
	return func(opts *ControllerOptions) (controllers.Reconciler, error) {
		modelCatalogController, err := controllers.NewModelCatalogController(&controllers.ModelCatalogControllerOption{
			Storage: opts.config.Storage,
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create model catalog controller")
		}

		return modelCatalogController, nil
	}
}

func NewModelRegistryControllerFactory() ControllerFactory {
	return func(opts *ControllerOptions) (controllers.Reconciler, error) {
		modelRegistryController, err := controllers.NewModelRegistryController(&controllers.ModelRegistryControllerOption{
			Storage: opts.config.Storage,
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create model registry controller")
		}

		return modelRegistryController, nil
	}
}
