package controllers

import (
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/gateway"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type ExternalEndpointController struct {
	storage     storage.Storage
	syncHandler func(ee *v1.ExternalEndpoint) error

	gw gateway.Gateway
}

type ExternalEndpointControllerOption struct {
	Storage storage.Storage
	Gw      gateway.Gateway
}

func NewExternalEndpointController(option *ExternalEndpointControllerOption) (*ExternalEndpointController, error) {
	c := &ExternalEndpointController{
		storage: option.Storage,
		gw:      option.Gw,
	}

	c.syncHandler = c.sync

	return c, nil
}

func (c *ExternalEndpointController) Reconcile(obj interface{}) error {
	ee, ok := obj.(*v1.ExternalEndpoint)
	if !ok {
		return errors.New("failed to assert obj to *v1.ExternalEndpoint")
	}

	klog.V(4).Info("Reconcile external_endpoint " + ee.Metadata.Name)

	return c.syncHandler(ee)
}

func (c *ExternalEndpointController) sync(obj *v1.ExternalEndpoint) error {
	var err error

	if obj.Metadata != nil && obj.Metadata.DeletionTimestamp != "" {
		isForceDelete := v1.IsForceDelete(obj.Metadata.Annotations)

		if obj.Status != nil && obj.Status.Phase == v1.ExternalEndpointPhaseDELETED {
			klog.Infof("ExternalEndpoint %s already marked as deleted, removing from DB", obj.Metadata.Name)

			err = c.storage.DeleteExternalEndpoint(obj.GetID())
			if err != nil {
				return errors.Wrapf(err, "failed to delete external_endpoint %s/%s from DB",
					obj.Metadata.Workspace, obj.Metadata.Name)
			}

			return nil
		}

		klog.Infof("Deleting external_endpoint %s (force=%v)", obj.Metadata.Name, isForceDelete)

		deleteErr := c.gw.DeleteExternalEndpoint(obj)

		updateErr := c.updateStatus(obj, v1.ExternalEndpointPhaseDELETED, deleteErr, nil)
		if updateErr != nil {
			klog.Errorf("failed to update external_endpoint %s/%s status: %v",
				obj.Metadata.Workspace, obj.Metadata.Name, updateErr)

			return errors.Wrapf(updateErr, "failed to update external_endpoint %s/%s status",
				obj.Metadata.Workspace, obj.Metadata.Name)
		}

		LogForceDeletionWarning(isForceDelete, "external_endpoint", obj.Metadata.Workspace, obj.Metadata.Name, deleteErr)

		if deleteErr != nil && !isForceDelete {
			return deleteErr
		}

		return nil
	}

	// sync external endpoint when not deleting
	upstreamStatuses, err := c.gw.SyncExternalEndpoint(obj)
	if err != nil {
		syncErr := c.updateStatus(obj, v1.ExternalEndpointPhaseFAILED, err, upstreamStatuses)
		if syncErr != nil {
			klog.Errorf("failed to update external_endpoint %s/%s status: %v",
				obj.Metadata.Workspace, obj.Metadata.Name, syncErr)
		}

		return errors.Wrapf(err, "failed to sync external_endpoint %s/%s to gateway",
			obj.Metadata.Workspace, obj.Metadata.Name)
	}

	// The gateway config was pushed, but individual upstreams may have been left
	// out because they no longer resolve (referenced endpoint or its cluster was
	// deleted). Report that as Degraded rather than Running: the endpoint serves,
	// yet some of its models do not. Requeueing is pointless here — recovery
	// depends on the user restoring the endpoint or fixing the reference — so the
	// reconcile succeeds and the detail lives in the status.
	phase := v1.ExternalEndpointPhaseRUNNING

	degradedCount, degradedErr := summarizeDegradedUpstreams(upstreamStatuses)
	if degradedCount > 0 {
		phase = v1.ExternalEndpointPhaseDEGRADED

		klog.Warningf("external_endpoint %s/%s is degraded: %d of %d upstreams unavailable",
			obj.Metadata.Workspace, obj.Metadata.Name, degradedCount, len(upstreamStatuses))
	}

	// Recompute status on every successful reconcile so that the service URL
	// stays in sync with the current gateway proxy address (e.g. after
	// neutree-core restarts with a different --gateway-proxy-url) and a
	// recovered upstream flips the phase back to Running. updateStatus
	// drift-detects and only writes when something user-visible has changed.
	err = c.updateStatus(obj, phase, degradedErr, upstreamStatuses)
	if err != nil {
		return errors.Wrapf(err, "failed to update external_endpoint %s/%s status to %s",
			obj.Metadata.Workspace, obj.Metadata.Name, phase)
	}

	return nil
}

// summarizeDegradedUpstreams reports how many upstreams failed to resolve and
// renders the endpoint-level error message for them. Per-upstream detail stays
// in the status list; this is the one-line version for list views. The error is
// nil when nothing is degraded.
func summarizeDegradedUpstreams(statuses []v1.ExternalEndpointUpstreamStatus) (int, error) {
	refs := make([]string, 0, len(statuses))

	for i, s := range statuses {
		if s.Phase != v1.ExternalEndpointUpstreamPhaseFailed {
			continue
		}

		// A spec entry with neither endpoint_ref nor upstream has no reference to
		// name, so fall back to its position rather than rendering an empty item.
		ref := s.Ref
		if ref == "" {
			ref = fmt.Sprintf("upstream #%d", i+1)
		}

		refs = append(refs, ref)
	}

	if len(refs) == 0 {
		return 0, nil
	}

	return len(refs), errors.Errorf(
		"%d upstream(s) unavailable and excluded from the gateway configuration: %s",
		len(refs), strings.Join(refs, ", "))
}

func (c *ExternalEndpointController) updateStatus(obj *v1.ExternalEndpoint, phase v1.ExternalEndpointPhase,
	err error, upstreamStatuses []v1.ExternalEndpointUpstreamStatus) error {
	serviceURL := ""
	if obj.Status != nil {
		serviceURL = obj.Status.ServiceURL
	}

	// A degraded endpoint is still served on its route, so its service URL must
	// stay fresh too.
	if phase == v1.ExternalEndpointPhaseRUNNING || phase == v1.ExternalEndpointPhaseDEGRADED {
		url, urlErr := c.gw.GetExternalEndpointServeUrl(obj)
		if urlErr != nil {
			klog.Warningf("failed to get external_endpoint %s/%s service url: %v",
				obj.Metadata.Workspace, obj.Metadata.Name, urlErr)
		} else if url != "" {
			serviceURL = url
		}
	}

	newStatus := &v1.ExternalEndpointStatus{
		LastTransitionTime: FormatStatusTime(),
		Phase:              phase,
		ServiceURL:         serviceURL,
		ErrorMessage:       FormatErrorForStatus(err),
		UpstreamStatuses:   upstreamStatuses,
	}

	if !externalEndpointStatusChanged(obj.Status, newStatus) {
		return nil
	}

	return c.storage.UpdateExternalEndpoint(obj.GetID(), &v1.ExternalEndpoint{Status: newStatus})
}

// externalEndpointStatusChanged reports whether the user-visible fields of the
// status changed between old and new. LastTransitionTime is intentionally
// excluded so the status row is only persisted when something meaningful moved.
func externalEndpointStatusChanged(old, newSt *v1.ExternalEndpointStatus) bool {
	if old == nil {
		return newSt != nil
	}

	if newSt == nil {
		return true
	}

	if old.Phase != newSt.Phase ||
		old.ServiceURL != newSt.ServiceURL ||
		old.ErrorMessage != newSt.ErrorMessage {
		return true
	}

	return upstreamStatusesChanged(old.UpstreamStatuses, newSt.UpstreamStatuses)
}

// upstreamStatusesChanged compares the per-upstream detail. The lists are built
// in spec order by the gateway, and the model names within an entry are sorted,
// so comparing them as a whole is stable across reconciles — and unlike a
// field-by-field compare it cannot go stale when the status type gains a field.
func upstreamStatusesChanged(old, newSt []v1.ExternalEndpointUpstreamStatus) bool {
	// An absent list and an empty list mean the same thing here, but marshal
	// differently (null vs []), so short-circuit before comparing.
	if len(old) == 0 && len(newSt) == 0 {
		return false
	}

	equal, diff, err := util.JsonEqual(old, newSt)
	if err != nil {
		// Comparison is only an optimization; on failure fall back to writing.
		klog.Warningf("failed to compare external_endpoint upstream statuses: %v", err)

		return true
	}

	if !equal {
		klog.V(4).Infof("external_endpoint upstream statuses changed, diff: %s", diff)
	}

	return !equal
}
