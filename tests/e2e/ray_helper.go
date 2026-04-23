package e2e

import (
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
)

// RayHelper provides Ray Dashboard API access for e2e tests.
type RayHelper struct {
	dashboardURL string
	service      dashboard.DashboardService
}

// NewRayHelper creates a RayHelper from a cluster dashboard URL.
func NewRayHelper(dashboardURL string) *RayHelper {
	return &RayHelper{
		dashboardURL: dashboardURL,
		service:      dashboard.NewDashboardService(dashboardURL),
	}
}

// GetServeApplications retrieves the current Ray Serve applications and their config.
func (h *RayHelper) GetServeApplications() (*dashboard.RayServeApplicationsResponse, error) {
	return h.service.GetServeApplications()
}
