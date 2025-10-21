package model_registry

import (
	"io"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type ListOption struct {
	Search string
	Limit  int
}

// ModelRegistry defines the interface for model registry operations
type ModelRegistry interface {
	// Basic registry operations
	ListModels(option ListOption) ([]v1.GeneralModel, error)
	Connect() error
	Disconnect() error
	HealthyCheck() error

	// Model operations
	GetModelVersion(name, version string) (*v1.ModelVersion, error)
	DeleteModel(name, version string) error
	ImportModel(reader io.Reader, name, version string, progress io.Writer) error
	ExportModel(name, version, outputPath string) error
	GetModelPath(name, version string) (string, error)
}

type NewModelRegistryFunc func(registry *v1.ModelRegistry) (ModelRegistry, error)

var (
	NewModelRegistry NewModelRegistryFunc = new
)

func new(registry *v1.ModelRegistry) (ModelRegistry, error) {
	switch registry.Spec.Type {
	case v1.HuggingFaceModelRegistryType:
		return newHuggingFace(registry)
	case v1.BentoMLModelRegistryType:
		return newFileBased(registry)
	default:
		return nil, errors.New("unsupported model registry type: " + string(registry.Spec.Type))
	}
}
