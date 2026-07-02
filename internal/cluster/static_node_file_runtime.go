package cluster

import (
	"context"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/util/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	commandrunner "github.com/neutree-ai/neutree/pkg/command_runner"
)

type StaticNodeFileRuntime struct {
	files commandrunner.FileClient
}

func NewStaticNodeFileRuntime(runner StaticNodeCommandRunner) StaticNodeFileRuntime {
	if runner == nil {
		return StaticNodeFileRuntime{}
	}

	return StaticNodeFileRuntime{files: runner.Files()}
}

func (r StaticNodeFileRuntime) WriteComponentConfigFiles(
	ctx context.Context,
	component v1.NodeComponentSpec,
) (bool, error) {
	if len(component.ConfigFiles) == 0 {
		return false, nil
	}

	files, err := r.fileClient()
	if err != nil {
		return false, err
	}

	changed := false

	for _, configFile := range component.ConfigFiles {
		fileChanged, err := files.WriteFileIfChanged(
			ctx,
			configFile.Path,
			[]byte(configFile.Content),
			commandrunner.WriteFileOptions{
				Mode:         configFile.Mode,
				Owner:        configFile.Owner,
				Group:        configFile.Group,
				Sudo:         configFile.Sudo,
				Atomic:       configFile.Atomic,
				CreateParent: configFile.CreateParent,
			},
		)
		if err != nil {
			return changed, errors.Wrapf(err, "failed to write config file %s", configFile.Path)
		}

		if fileChanged && !configFile.SkipRestartOnChange {
			changed = true
		}
	}

	return changed, nil
}

func (r StaticNodeFileRuntime) RemoveComponentConfigFiles(
	ctx context.Context,
	component v1.NodeComponentSpec,
) error {
	if len(component.ConfigFiles) == 0 {
		return nil
	}

	files, err := r.fileClient()
	if err != nil {
		return err
	}

	errs := []error{}

	for _, configFile := range component.ConfigFiles {
		if err := files.Remove(ctx, configFile.Path, commandrunner.RemoveFileOptions{Sudo: configFile.Sudo}); err != nil {
			errs = append(errs, errors.Wrapf(err, "failed to remove config file %s", configFile.Path))
		}
	}

	return apierrors.NewAggregate(errs)
}

func (r StaticNodeFileRuntime) fileClient() (commandrunner.FileClient, error) {
	if r.files == nil {
		return nil, errors.New("static node file runtime is not configured")
	}

	return r.files, nil
}
