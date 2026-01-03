package cluster

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/deploy"
	"github.com/neutree-ai/neutree/internal/util"
)

func (c *NativeKubernetesClusterReconciler) reconcileModelCache(reconcileCtx *ReconcileContext) error {
	err := c.reconcileModelCacheResources(reconcileCtx)
	if err != nil {
		return errors.Wrapf(err, "failed to reconcile model cache resource")
	}

	return c.reconcileModelCacheStatus(reconcileCtx)
}

func (c *NativeKubernetesClusterReconciler) reconcileModelCacheResources(reconcileCtx *ReconcileContext) error {
	objList, err := c.getModelCacheResources(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to get model cache resources")
	}

	applier := deploy.NewKubernetesDeployer(
		reconcileCtx.ctrClient,
		reconcileCtx.clusterNamespace,
		reconcileCtx.Cluster.Metadata.Name, // resourceName
		"modelcache",                       // componentName
	).
		WithNewObjects(objList).
		WithLabels(map[string]string{
			v1.NeutreeClusterLabelKey:          reconcileCtx.Cluster.Metadata.Name,
			v1.NeutreeClusterWorkspaceLabelKey: reconcileCtx.Cluster.Metadata.Workspace,
			v1.LabelManagedBy:                  v1.LabelManagedByValue,
		}).
		WithLogger(reconcileCtx.logger)

	changedCount, err := applier.Apply(reconcileCtx.Ctx)
	if err != nil {
		return errors.Wrap(err, "failed to apply model cache")
	}

	if changedCount > 0 {
		reconcileCtx.logger.Info("Applied model cache manifests", "changedObjects", changedCount)
	}

	return nil
}

func (c *NativeKubernetesClusterReconciler) getModelCacheResources(reconcileCtx *ReconcileContext) (*unstructured.UnstructuredList, error) {
	var (
		cacheLabel = map[string]string{
			"cluster":   reconcileCtx.Cluster.Metadata.Name,
			"workspace": reconcileCtx.Cluster.Metadata.Workspace,
		}
		objList = &unstructured.UnstructuredList{}
	)

	// ModelCaches is now in ClusterConfig level
	if reconcileCtx.Cluster.Spec.Config == nil || reconcileCtx.Cluster.Spec.Config.ModelCaches == nil {
		return objList, nil
	}

	var errs []error

	for _, cache := range reconcileCtx.Cluster.Spec.Config.ModelCaches {
		if cache.PVC != nil {
			pvcSpec := applyDefault(*cache.PVC)

			err := validatePVCSpec(pvcSpec)
			if err != nil {
				errs = append(errs, errors.Wrap(err, "invalid pvc spec for model cache"))
				continue
			}

			pvc := &corev1.PersistentVolumeClaim{
				TypeMeta: metav1.TypeMeta{
					Kind:       "PersistentVolumeClaim",
					APIVersion: "v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      util.CacheName(cache),
					Namespace: reconcileCtx.clusterNamespace,
					Labels:    cacheLabel,
				},
				Spec: pvcSpec,
			}

			u, err := util.ToUnstructured(pvc)
			if err != nil {
				errs = append(errs, errors.Wrap(err, "failed to convert pvc to unstructured"))
				continue
			}

			objList.Items = append(objList.Items, *u)
		}
	}

	if len(errs) > 0 {
		return nil, utilerrors.NewAggregate(errs)
	}

	return objList, nil
}

func (c *NativeKubernetesClusterReconciler) reconcileModelCacheStatus(
	reconcileCtx *ReconcileContext,
) error {
	// ModelCaches is now in ClusterConfig level
	if reconcileCtx.Cluster.Spec.Config == nil || reconcileCtx.Cluster.Spec.Config.ModelCaches == nil {
		return nil
	}

	var errs []error

	for _, cache := range reconcileCtx.Cluster.Spec.Config.ModelCaches {
		// only pvc need check status.
		if cache.PVC != nil {
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      util.CacheName(cache),
					Namespace: reconcileCtx.clusterNamespace,
				},
			}

			ready, err := pvcStatus(reconcileCtx.Ctx, reconcileCtx.ctrClient, pvc)
			if err != nil {
				errs = append(errs, errors.Wrapf(err, "failed to get model cache pvc %s status", util.CacheName(cache)))
			}

			if !ready {
				errs = append(errs, errors.Errorf("model cache pvc %s is not ready to use now", util.CacheName(cache)))
			}
		}
	}

	if len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}

	return nil
}

func validatePVCSpec(pvcSpec corev1.PersistentVolumeClaimSpec) error {
	if len(pvcSpec.AccessModes) == 0 {
		return errors.New("pvc access modes must be specified")
	}

	if len(pvcSpec.AccessModes) != 1 {
		return errors.New("only one pvc access mode is supported")
	}

	supportedMode := corev1.ReadWriteMany
	if pvcSpec.AccessModes[0] != supportedMode {
		return fmt.Errorf("only %s pvc access mode is supported", supportedMode)
	}

	if pvcSpec.Resources.Requests == nil {
		return errors.New("pvc resource requests must be specified")
	}

	if _, exists := pvcSpec.Resources.Requests[corev1.ResourceStorage]; !exists {
		return errors.New("pvc storage request must be specified")
	}

	if pvcSpec.VolumeMode == nil {
		return errors.New("pvc volume mode must be specified")
	}

	if *pvcSpec.VolumeMode != corev1.PersistentVolumeFilesystem {
		return errors.New("only filesystem volume mode is supported")
	}

	return nil
}

func applyDefault(pvcSpec corev1.PersistentVolumeClaimSpec) corev1.PersistentVolumeClaimSpec {
	if len(pvcSpec.AccessModes) == 0 {
		pvcSpec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}
	}

	if pvcSpec.Resources.Requests == nil {
		pvcSpec.Resources.Requests = corev1.ResourceList{}
	}

	if _, exists := pvcSpec.Resources.Requests[corev1.ResourceStorage]; !exists {
		pvcSpec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("10Gi")
	}

	if pvcSpec.VolumeMode == nil {
		fileSystemMode := corev1.PersistentVolumeFilesystem
		pvcSpec.VolumeMode = &fileSystemMode
	}

	return pvcSpec
}

func pvcStatus(ctx context.Context, ctrClient client.Client, pvc *corev1.PersistentVolumeClaim) (bool, error) {
	err := ctrClient.Get(ctx, client.ObjectKeyFromObject(pvc), pvc)
	if err != nil {
		return false, errors.Wrap(err, "failed to get pvc status")
	}

	if pvc.Status.Phase != corev1.ClaimBound {
		return false, nil
	}

	// check pv
	pvName := pvc.Spec.VolumeName
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: pvName,
		},
	}

	err = ctrClient.Get(ctx, client.ObjectKeyFromObject(pv), pv)
	if err != nil {
		return false, errors.Wrap(err, "failed to get related pv")
	}

	if pv.Spec.Capacity == nil {
		return false, errors.New("pv capacity is not specified")
	}

	if pvc.Spec.Resources.Requests == nil {
		return false, errors.New("pvc storage request is not specified")
	}

	if pv.Spec.Capacity.Storage().Cmp(*pvc.Spec.Resources.Requests.Storage()) < 0 {
		return false, nil
	}

	return true, nil
}
