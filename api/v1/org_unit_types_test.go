package v1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrgUnit_IsActive(t *testing.T) {
	assert.True(t, (&OrgUnit{}).IsActive(), "no status yet")
	assert.True(t, (&OrgUnit{Status: &OrgUnitStatus{Phase: OrgPhaseActive}}).IsActive())
	assert.False(t, (&OrgUnit{Status: &OrgUnitStatus{Phase: OrgPhaseInactive}}).IsActive())

	assert.True(t, (&Team{}).IsActive(), "no status yet")
	assert.False(t, (&Team{Status: &TeamStatus{Phase: OrgPhaseInactive}}).IsActive())
}

func TestOrgUnit_JSON(t *testing.T) {
	// A sync write without a path must not send one: the database owns it.
	raw, err := json.Marshal(&OrgUnit{Spec: &OrgUnitSpec{IdentitySource: "corp", ExternalID: "42"}})
	require.NoError(t, err)
	assert.NotContains(t, string(raw), `"path"`)
	assert.NotContains(t, string(raw), `"parent"`)

	var unit OrgUnit
	require.NoError(t, json.Unmarshal([]byte(`{"id":7,"path":"/3/7/","spec":{"identity_source":"corp","external_id":"42","parent":"dept-3"},"status":{"phase":"Inactive"}}`), &unit))
	assert.Equal(t, "7", unit.GetID())
	assert.Equal(t, "/3/7/", unit.Path)
	assert.Equal(t, "dept-3", unit.Spec.Parent)
	assert.False(t, unit.IsActive())
}

func TestOrgLists(t *testing.T) {
	units := &OrgUnitList{Items: []OrgUnit{{ID: 1}, {ID: 2}}}
	assert.Len(t, units.GetItems(), 2)

	teams := &TeamList{}
	teams.SetItems((&TeamList{Items: []Team{{ID: 3}}}).GetItems())
	require.Len(t, teams.Items, 1)
	assert.Equal(t, 3, teams.Items[0].ID)
}
