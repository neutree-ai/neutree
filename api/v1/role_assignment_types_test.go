package v1

import (
	"encoding/json"
	"testing"
)

// A group assignment must not send user_id: an empty string is not a UUID and
// the database would reject the whole spec.
func TestRoleAssignmentSpec_JSON(t *testing.T) {
	tests := []struct {
		name string
		spec RoleAssignmentSpec
		want string
	}{
		{
			name: "user",
			spec: RoleAssignmentSpec{UserID: "u1", Global: true, Role: "admin"},
			want: `{"user_id":"u1","global":true,"role":"admin"}`,
		},
		{
			name: "team",
			spec: RoleAssignmentSpec{Global: true, Role: "workspace-user", SubjectKind: RoleAssignmentSubjectTeam, SubjectName: "core"},
			want: `{"global":true,"role":"workspace-user","subject_kind":"Team","subject_name":"core"}`,
		},
		{
			name: "org unit",
			spec: RoleAssignmentSpec{Role: "department-admin", OrgUnit: "sales", SubjectKind: RoleAssignmentSubjectOrgUnit, SubjectName: "sales"},
			want: `{"role":"department-admin","org_unit":"sales","subject_kind":"OrgUnit","subject_name":"sales"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(tt.spec)
			if err != nil {
				t.Fatalf("Marshal failed: %v", err)
			}

			if string(raw) != tt.want {
				t.Errorf("Marshal = %s, want %s", raw, tt.want)
			}

			var back RoleAssignmentSpec
			if err := json.Unmarshal(raw, &back); err != nil || back != tt.spec {
				t.Errorf("round trip = %+v, %v; want %+v", back, err, tt.spec)
			}
		})
	}
}
