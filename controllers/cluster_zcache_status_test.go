package controllers

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestClusterStatusRetainsZCacheAfterUnrelatedComponentError(t *testing.T) {
	s := &storagemocks.MockStorage{}
	status := &v1.ZCacheStatus{Phase: "Reconciling", Nodes: []v1.ZCacheNode{{Name: "worker", Runtime: "Ready"}}, Change: &v1.ZCacheChange{OperationID: "op-1"}}
	s.On("UpdateCluster", "1", mock.MatchedBy(func(c *v1.Cluster) bool {
		return c.Status.ZCache == status && c.Status.ErrorMessage == "router unavailable"
	})).Return(nil).Once()
	controller := &ClusterController{storage: s}
	c := &v1.Cluster{ID: 1, Status: &v1.ClusterStatus{ZCache: status}}
	require.NoError(t, controller.updateStatus(c, v1.ClusterPhaseFailed, errors.New("router unavailable")))
	s.AssertExpectations(t)
}
