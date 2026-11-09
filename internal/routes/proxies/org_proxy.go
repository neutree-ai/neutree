package proxies

import (
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// RegisterOrgUnitRoutes registers OrgUnit routes (read-only; sync writes them).
//
// Allowed methods: GET
func RegisterOrgUnitRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/org_units")
	proxyGroup.Use(middlewares...)

	proxyGroup.GET("", CreateStructProxyHandler[v1.OrgUnit](deps, storage.ORG_UNIT_TABLE))
}

// RegisterTeamRoutes registers Team routes; the database keeps synced Teams
// read-only.
//
// Allowed methods: GET, POST, PATCH, DELETE
func RegisterTeamRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/teams")
	proxyGroup.Use(middlewares...)

	handler := CreateStructProxyHandler[v1.Team](deps, storage.TEAM_TABLE)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", handler)
	proxyGroup.DELETE("", handler)
}

// RegisterTeamMemberRoutes registers Team membership routes. Writes add or
// remove members of Teams created in Neutree; members of synced Teams are
// read-only in the database.
//
// Allowed methods: GET, POST, DELETE
func RegisterTeamMemberRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/team_members")
	proxyGroup.Use(middlewares...)

	handler := CreateStructProxyHandler[storage.TeamMember](deps, storage.TEAM_MEMBER_TABLE)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.DELETE("", handler)
}

// RegisterOrgUnitMemberRoutes registers OrgUnit membership routes; writes
// assign a local account by hand, and RLS decides who may write which row.
//
// Allowed methods: GET, POST, PATCH, DELETE
func RegisterOrgUnitMemberRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/org_unit_members")
	proxyGroup.Use(middlewares...)

	handler := CreateStructProxyHandler[storage.OrgUnitMember](deps, storage.ORG_UNIT_MEMBER_TABLE)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", handler)
	proxyGroup.DELETE("", handler)
}
