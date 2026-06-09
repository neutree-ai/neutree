package hami

import (
	"context"
	"time"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

func (h *HAMiComponent) replaceWorkloadsWithImmutableSelectorChanges(
	ctx context.Context,
	objs *unstructured.UnstructuredList,
) error {
	workloads := []struct {
		kind string
		name string
	}{
		{kind: "Deployment", name: SchedulerName},
		{kind: "DaemonSet", name: DevicePluginDaemonSetName},
	}

	for _, workload := range workloads {
		desired, ok := findRenderedWorkload(objs, workload.kind, workload.name)
		if !ok {
			continue
		}

		desiredSelector, err := renderedWorkloadSelector(desired)
		if err != nil {
			return err
		}

		current, err := h.getCurrentWorkload(ctx, workload.kind, workload.name)
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}

			return errors.Wrapf(err, "failed to get HAMi %s %s", workload.kind, workload.name)
		}
		if current.GetLabels()[ManagedComponentLabelKey] != ManagedComponentLabelValue {
			continue
		}

		currentSelector, err := workloadSelector(current)
		if err != nil {
			return err
		}
		if equality.Semantic.DeepEqual(currentSelector, desiredSelector) {
			continue
		}

		h.logger.Info("Replacing HAMi workload because selector changed", "kind", workload.kind, "name", workload.name)
		if err := h.ctrlClient.Delete(ctx, current); err != nil && !apierrors.IsNotFound(err) {
			return errors.Wrapf(err, "failed to delete HAMi %s %s before selector migration", workload.kind, workload.name)
		}
		if err := wait.PollUntilContextTimeout(ctx, time.Second, 30*time.Second, true,
			func(ctx context.Context) (bool, error) {
				_, err := h.getCurrentWorkload(ctx, workload.kind, workload.name)
				if apierrors.IsNotFound(err) {
					return true, nil
				}
				if err != nil {
					return false, err
				}

				return false, nil
			}); err != nil {
			return errors.Wrapf(err, "failed to wait for HAMi %s %s deletion", workload.kind, workload.name)
		}
	}

	return nil
}

func (h *HAMiComponent) getCurrentWorkload(ctx context.Context, kind, name string) (clientObject, error) {
	key := types.NamespacedName{Name: name, Namespace: h.namespace}
	switch kind {
	case "Deployment":
		deployment := &appsv1.Deployment{}
		err := h.ctrlClient.Get(ctx, key, deployment)
		return deployment, err
	case "DaemonSet":
		daemonSet := &appsv1.DaemonSet{}
		err := h.ctrlClient.Get(ctx, key, daemonSet)
		return daemonSet, err
	default:
		return nil, errors.Errorf("unsupported HAMi workload kind %s", kind)
	}
}

type clientObject interface {
	runtime.Object
	metav1.Object
}

func workloadSelector(obj clientObject) (*metav1.LabelSelector, error) {
	switch workload := obj.(type) {
	case *appsv1.Deployment:
		return workload.Spec.Selector, nil
	case *appsv1.DaemonSet:
		return workload.Spec.Selector, nil
	default:
		return nil, errors.Errorf("unsupported HAMi workload type %T", obj)
	}
}

func findRenderedWorkload(objs *unstructured.UnstructuredList, kind, name string) (*unstructured.Unstructured, bool) {
	if objs == nil {
		return nil, false
	}

	for i := range objs.Items {
		obj := &objs.Items[i]
		if obj.GetKind() == kind && obj.GetName() == name {
			return obj, true
		}
	}

	return nil, false
}

func renderedWorkloadSelector(obj *unstructured.Unstructured) (*metav1.LabelSelector, error) {
	rawSelector, found, err := unstructured.NestedMap(obj.Object, "spec", "selector")
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read rendered %s %s selector", obj.GetKind(), obj.GetName())
	}
	if !found {
		return nil, errors.Errorf("rendered %s %s has no selector", obj.GetKind(), obj.GetName())
	}

	selector := &metav1.LabelSelector{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rawSelector, selector); err != nil {
		return nil, errors.Wrapf(err, "failed to parse rendered %s %s selector", obj.GetKind(), obj.GetName())
	}

	return selector, nil
}
