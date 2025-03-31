package model_registry

import (
	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type ListOption struct {
	Search string
	Limit  int
}

type ModelRegistry interface {
	ListModels(option ListOption) ([]v1.GeneralModel, error)
	Connect() error
	Disconnect() error
	HealthyCheck() bool
}

type NewModelRegistry func(registry *v1.ModelRegistry) (ModelRegistry, error)

func New(registry *v1.ModelRegistry) (ModelRegistry, error) {
	switch registry.Spec.Type {
	case v1.HuggingFaceModelRegistryType:
		return newHuggingFace(registry), nil
	case v1.BentoMLModelRegistryType:
		return newFileBased(registry)
	default:
		return nil, errors.New("unsupported model registry type: " + string(registry.Spec.Type))
	}
}
