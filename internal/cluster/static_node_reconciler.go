package cluster

import (
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	warmReasonImageInspectFailed = "ImageInspectFailed"
	warmReasonImagePullFailed    = "ImagePullFailed"
	warmReasonImagePulled        = "ImagePulled"
	warmReasonImageReady         = "ImageReady"
)

type StaticNodeCommandRunner interface {
	Run(ctx context.Context, command string) (string, error)
}

type StaticNodeReconciler struct{}

func (r *StaticNodeReconciler) ReconcileWarmImages(
	ctx context.Context,
	node *v1.StaticNode,
	runner StaticNodeCommandRunner,
) (*v1.WarmStatus, error) {
	if node == nil || node.Spec == nil || node.Spec.Warm == nil || len(node.Spec.Warm.Images) == 0 {
		return &v1.WarmStatus{Ready: true}, nil
	}

	if runner == nil {
		return nil, errors.New("static node command runner is required")
	}

	status := &v1.WarmStatus{
		Ready:  true,
		Images: make([]v1.WarmImageStatus, 0, len(node.Spec.Warm.Images)),
	}

	for _, image := range node.Spec.Warm.Images {
		imageStatus, err := r.reconcileWarmImage(ctx, image, runner)
		status.Images = append(status.Images, imageStatus)

		if err != nil && image.Required {
			status.Ready = false
			status.Reason = imageStatus.Reason
			status.Message = imageStatus.Message

			return status, err
		}

		if image.Required && !imageStatus.Ready {
			status.Ready = false
		}
	}

	return status, nil
}

func (r *StaticNodeReconciler) reconcileWarmImage(
	ctx context.Context,
	image v1.WarmImageSpec,
	runner StaticNodeCommandRunner,
) (v1.WarmImageStatus, error) {
	status := v1.WarmImageStatus{
		Name:  image.Name,
		Ref:   image.Ref,
		Phase: v1.WarmPhasePending,
	}

	if image.Ref == "" {
		status.Phase = v1.WarmPhaseFailed
		status.Reason = warmReasonImageInspectFailed
		status.Message = "warm image ref is required"

		return status, errors.New(status.Message)
	}

	digest, err := inspectDockerImage(ctx, runner, image.Ref)
	if err == nil && digest != "" {
		status.Ready = true
		status.Digest = digest
		status.Phase = v1.WarmPhaseReady
		status.Reason = warmReasonImageReady

		return status, nil
	}

	status.Phase = v1.WarmPhasePulling
	if _, pullErr := runner.Run(ctx, "docker pull "+shellArg(image.Ref)); pullErr != nil {
		status.Phase = v1.WarmPhaseFailed
		status.Reason = warmReasonImagePullFailed
		status.Message = fmt.Sprintf("failed to pull image %s: %v", image.Ref, pullErr)

		return status, pullErr
	}

	digest, err = inspectDockerImage(ctx, runner, image.Ref)
	if err != nil || digest == "" {
		status.Phase = v1.WarmPhaseFailed
		status.Reason = warmReasonImageInspectFailed
		status.Message = fmt.Sprintf("failed to inspect image %s after pull", image.Ref)

		if err != nil {
			status.Message += ": " + err.Error()
		}

		return status, errors.New(status.Message)
	}

	status.Ready = true
	status.Digest = digest
	status.Phase = v1.WarmPhaseReady
	status.Reason = warmReasonImagePulled

	return status, nil
}

func inspectDockerImage(ctx context.Context, runner StaticNodeCommandRunner, imageRef string) (string, error) {
	output, err := runner.Run(ctx, "docker image inspect --format='{{index .RepoDigests 0}}' "+shellArg(imageRef))
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(output), nil
}

func shellArg(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
