package app

import (
	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
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

	scheme  *scheme.Scheme
	storage storage.ObjectStorage
}

// ControllerFactory defines a function type for creating controllers
type ControllerFactory func(opts *ControllerOptions) (controllers.Controller, error)

func NewClusterControllerFactory() ControllerFactory {
	return func(opts *ControllerOptions) (controllers.Controller, error) {
		clusterController, err := controllers.NewClusterController(
			&controllers.ClusterControllerOption{
				Storage:                 opts.config.Storage,
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

		// Create a new controller with the provided options
		ctrl := controllers.NewController(opts.name,
			controllers.WithWorkers(opts.config.ControllerConfig.Workers),
			controllers.WithBeforeReconcileHook(opts.beforeHooks),
			controllers.WithAfterReconcileHook(opts.afterHooks),
			controllers.WithReconciler(clusterController),
			controllers.WithObject(&v1.Cluster{}),
			controllers.WithScheme(opts.scheme),
			controllers.WithStorage(opts.storage),
		)

		return ctrl, nil
	}
}

func NewEngineControllerFactory() ControllerFactory {
	return func(opts *ControllerOptions) (controllers.Controller, error) {
		engineController, err := controllers.NewEngineController(&controllers.EngineControllerOption{
			Storage: opts.config.Storage,
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create engine controller")
		}

		ctrl := controllers.NewController(opts.name,
			controllers.WithWorkers(opts.config.ControllerConfig.Workers),
			controllers.WithBeforeReconcileHook(opts.beforeHooks),
			controllers.WithAfterReconcileHook(opts.afterHooks),
			controllers.WithReconciler(engineController),
			controllers.WithObject(&v1.Engine{}),
			controllers.WithScheme(opts.scheme),
			controllers.WithStorage(opts.storage),
		)

		return ctrl, nil
	}
}

func NewEndpointControllerFactory() ControllerFactory {
	return func(opts *ControllerOptions) (controllers.Controller, error) {
		endpointController, err := controllers.NewEndpointController(&controllers.EndpointControllerOption{
			Storage:        opts.config.Storage,
			Gw:             opts.config.Gateway,
			AcceleratorMgr: opts.config.AcceleratorManager,
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create endpoint controller")
		}

		ctrl := controllers.NewController(opts.name,
			controllers.WithWorkers(opts.config.ControllerConfig.Workers),
			controllers.WithBeforeReconcileHook(opts.beforeHooks),
			controllers.WithAfterReconcileHook(opts.afterHooks),
			controllers.WithReconciler(endpointController),
			controllers.WithObject(&v1.Endpoint{}),
			controllers.WithScheme(opts.scheme),
			controllers.WithStorage(opts.storage),
		)

		return ctrl, nil
	}
}

func NewRoleControllerFactory() ControllerFactory {
	return func(opts *ControllerOptions) (controllers.Controller, error) {
		roleController, err := controllers.NewRoleController(&controllers.RoleControllerOption{
			Storage: opts.config.Storage,
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create role controller")
		}

		ctrl := controllers.NewController(opts.name,
			controllers.WithWorkers(opts.config.ControllerConfig.Workers),
			controllers.WithBeforeReconcileHook(opts.beforeHooks),
			controllers.WithAfterReconcileHook(opts.afterHooks),
			controllers.WithReconciler(roleController),
			controllers.WithObject(&v1.Role{}),
			controllers.WithScheme(opts.scheme),
			controllers.WithStorage(opts.storage),
		)

		return ctrl, nil
	}
}

func NewRoleAssignmentControllerFactory() ControllerFactory {
	return func(opts *ControllerOptions) (controllers.Controller, error) {
		roleAssignmentController, err := controllers.NewRoleAssignmentController(&controllers.RoleAssignmentControllerOption{
			Storage: opts.config.Storage,
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create role assignment controller")
		}

		ctrl := controllers.NewController(opts.name,
			controllers.WithWorkers(opts.config.ControllerConfig.Workers),
			controllers.WithBeforeReconcileHook(opts.beforeHooks),
			controllers.WithAfterReconcileHook(opts.afterHooks),
			controllers.WithReconciler(roleAssignmentController),
			controllers.WithObject(&v1.RoleAssignment{}),
			controllers.WithScheme(opts.scheme),
			controllers.WithStorage(opts.storage),
		)

		return ctrl, nil
	}
}

func NewWorkspaceControllerFactory() ControllerFactory {
	return func(opts *ControllerOptions) (controllers.Controller, error) {
		workspaceController, err := controllers.NewWorkspaceController(&controllers.WorkspaceControllerOption{
			Storage:            opts.config.Storage,
			AcceleratorManager: opts.config.AcceleratorManager,
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create workspace controller")
		}

		ctrl := controllers.NewController(opts.name,
			controllers.WithWorkers(opts.config.ControllerConfig.Workers),
			controllers.WithBeforeReconcileHook(opts.beforeHooks),
			controllers.WithAfterReconcileHook(opts.afterHooks),
			controllers.WithReconciler(workspaceController),
			controllers.WithObject(&v1.Workspace{}),
			controllers.WithScheme(opts.scheme),
			controllers.WithStorage(opts.storage),
		)

		return ctrl, nil
	}
}

func NewApiKeyControllerFactory() ControllerFactory {
	return func(opts *ControllerOptions) (controllers.Controller, error) {
		apiKeyController, err := controllers.NewApiKeyController(&controllers.ApiKeyControllerOption{
			Storage: opts.config.Storage,
			Gw:      opts.config.Gateway,
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create api key controller")
		}

		ctrl := controllers.NewController(opts.name,
			controllers.WithWorkers(opts.config.ControllerConfig.Workers),
			controllers.WithBeforeReconcileHook(opts.beforeHooks),
			controllers.WithAfterReconcileHook(opts.afterHooks),
			controllers.WithReconciler(apiKeyController),
			controllers.WithObject(&v1.ApiKey{}),
			controllers.WithScheme(opts.scheme),
			controllers.WithStorage(opts.storage),
		)

		return ctrl, nil
	}
}

func NewImageRegistryControllerFactory() ControllerFactory {
	return func(opts *ControllerOptions) (controllers.Controller, error) {
		imageRegistryController, err := controllers.NewImageRegistryController(&controllers.ImageRegistryControllerOption{
			Storage:      opts.config.Storage,
			ImageService: opts.config.ImageService,
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create image registry controller")
		}

		ctrl := controllers.NewController(opts.name,
			controllers.WithWorkers(opts.config.ControllerConfig.Workers),
			controllers.WithBeforeReconcileHook(opts.beforeHooks),
			controllers.WithAfterReconcileHook(opts.afterHooks),
			controllers.WithReconciler(imageRegistryController),
			controllers.WithObject(&v1.ImageRegistry{}),
			controllers.WithScheme(opts.scheme),
			controllers.WithStorage(opts.storage),
		)

		return ctrl, nil
	}
}

func NewModelCatalogControllerFactory() ControllerFactory {
	return func(opts *ControllerOptions) (controllers.Controller, error) {
		modelCatalogController, err := controllers.NewModelCatalogController(&controllers.ModelCatalogControllerOption{
			Storage: opts.config.Storage,
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create model catalog controller")
		}

		ctrl := controllers.NewController(opts.name,
			controllers.WithWorkers(opts.config.ControllerConfig.Workers),
			controllers.WithBeforeReconcileHook(opts.beforeHooks),
			controllers.WithAfterReconcileHook(opts.afterHooks),
			controllers.WithReconciler(modelCatalogController),
			controllers.WithObject(&v1.ModelCatalog{}),
			controllers.WithScheme(opts.scheme),
			controllers.WithStorage(opts.storage),
		)

		return ctrl, nil
	}
}

func NewModelRegistryControllerFactory() ControllerFactory {
	return func(opts *ControllerOptions) (controllers.Controller, error) {
		modelRegistryController, err := controllers.NewModelRegistryController(&controllers.ModelRegistryControllerOption{
			Storage: opts.config.Storage,
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create model registry controller")
		}

		ctrl := controllers.NewController(opts.name,
			controllers.WithWorkers(opts.config.ControllerConfig.Workers),
			controllers.WithBeforeReconcileHook(opts.beforeHooks),
			controllers.WithAfterReconcileHook(opts.afterHooks),
			controllers.WithReconciler(modelRegistryController),
			controllers.WithObject(&v1.ModelRegistry{}),
			controllers.WithScheme(opts.scheme),
			controllers.WithStorage(opts.storage),
		)

		return ctrl, nil
	}
}

func NewUserProfileControllerFactory() ControllerFactory {
	return func(opts *ControllerOptions) (controllers.Controller, error) {
		userProfileController, err := controllers.NewUserProfileController(&controllers.UserProfileControllerOption{
			Storage:    opts.config.Storage,
			AuthClient: opts.config.AuthClient,
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create user profile controller")
		}

		ctrl := controllers.NewController(opts.name,
			controllers.WithWorkers(opts.config.ControllerConfig.Workers),
			controllers.WithBeforeReconcileHook(opts.beforeHooks),
			controllers.WithAfterReconcileHook(opts.afterHooks),
			controllers.WithReconciler(userProfileController),
			controllers.WithObject(&v1.UserProfile{}),
			controllers.WithScheme(opts.scheme),
			controllers.WithStorage(opts.storage),
		)

		return ctrl, nil
	}
}
