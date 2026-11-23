package proxies

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestOrgRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotMethod, gotPath, gotQuery string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(upstream.Close)

	deps := &Dependencies{StorageAccessURL: upstream.URL}
	router := gin.New()
	group := router.Group("/api/v1")
	RegisterOrgUnitRoutes(group, nil, deps)
	RegisterTeamRoutes(group, nil, deps)
	RegisterTeamMemberRoutes(group, nil, deps)
	RegisterOrgUnitMemberRoutes(group, nil, deps)
	RegisterGroupPolicyRoutes(group, nil, deps)
	RegisterServiceAccountRoutes(group, nil, deps)
	RegisterTeamApplicationPermissionRoutes(group, nil, deps)

	cases := []struct {
		method    string
		table     string
		forwarded bool
	}{
		{http.MethodGet, "org_units", true},
		{http.MethodPost, "org_units", false},
		{http.MethodPatch, "org_units", false},
		{http.MethodDelete, "org_units", false},
		{http.MethodGet, "teams", true},
		{http.MethodPost, "teams", true},
		{http.MethodPatch, "teams", true},
		{http.MethodDelete, "teams", true},
		{http.MethodPut, "teams", false},
		{http.MethodGet, "team_members", true},
		{http.MethodPost, "team_members", true},
		{http.MethodDelete, "team_members", true},
		{http.MethodPatch, "team_members", false},
		{http.MethodGet, "org_unit_members", true},
		{http.MethodPost, "org_unit_members", true},
		{http.MethodPatch, "org_unit_members", true},
		{http.MethodDelete, "org_unit_members", true},
		{http.MethodGet, "group_policies", true},
		{http.MethodPost, "group_policies", true},
		{http.MethodPatch, "group_policies", true},
		{http.MethodDelete, "group_policies", true},
		{http.MethodPut, "group_policies", false},
		{http.MethodGet, "service_accounts", true},
		{http.MethodPost, "service_accounts", true},
		{http.MethodPatch, "service_accounts", true},
		{http.MethodDelete, "service_accounts", true},
		{http.MethodPut, "service_accounts", false},
		{http.MethodGet, "team_application_permissions", true},
		{http.MethodPost, "team_application_permissions", true},
		{http.MethodPatch, "team_application_permissions", true},
		{http.MethodDelete, "team_application_permissions", true},
	}

	for _, c := range cases {
		t.Run(c.method+" "+c.table, func(t *testing.T) {
			gotMethod, gotPath, gotQuery = "", "", ""

			query := "select=*,org_units(*)&user_id=eq.u1"
			req := httptest.NewRequest(c.method, "/api/v1/"+c.table+"?"+query, strings.NewReader(`{"org_unit_id":1}`))
			req.Header.Set("Content-Type", "application/json")

			rec := newCloseNotifyRecorder()
			router.ServeHTTP(rec, req)

			if !c.forwarded {
				assert.Equal(t, http.StatusNotFound, rec.ResponseRecorder.Code)
				assert.Empty(t, gotMethod)

				return
			}

			assert.Equal(t, http.StatusOK, rec.ResponseRecorder.Code)
			assert.Equal(t, c.method, gotMethod)
			assert.Equal(t, "/"+c.table, gotPath)
			assert.Equal(t, query, gotQuery)
		})
	}
}
