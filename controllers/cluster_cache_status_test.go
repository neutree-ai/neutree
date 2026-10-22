package controllers

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestClusterStatusRetainsCacheAfterUnrelatedComponentError(t *testing.T) {
	s := &storagemocks.MockStorage{}
	status := &v1.ClusterCacheStatus{Provider: "example", Details: json.RawMessage(`{"operation_id":"op-1"}`)}
	s.On("UpdateCluster", "1", mock.MatchedBy(func(c *v1.Cluster) bool {
		return c.Status.Cache == status && c.Status.ErrorMessage == "router unavailable"
	})).Return(nil).Once()
	controller := &ClusterController{storage: s}
	c := &v1.Cluster{ID: 1, Status: &v1.ClusterStatus{Cache: status}}
	require.NoError(t, controller.updateStatus(c, v1.ClusterPhaseFailed, errors.New("router unavailable")))
	s.AssertExpectations(t)
}
