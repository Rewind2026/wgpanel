package domain

import "testing"

func TestRolePermissionMatrix(t *testing.T) {
	cases := []struct {
		role  Role
		perm  Permission
		allow bool
	}{
		{RoleOwner, PermSystemShutdown, true},
		{RoleOwner, PermAuditRead, true},
		{RoleAdmin, PermSettingsWrite, true},
		{RoleAdmin, PermAuditRead, true},
		{RoleOperator, PermPeerCreate, true},
		{RoleOperator, PermConfigExport, true},
		{RoleOperator, PermAuditRead, false},
		{RoleOperator, PermSystemShutdown, false},
		{RoleViewer, PermServerRead, true},
		{RoleViewer, PermPeerRead, true},
		{RoleViewer, PermMetricsRead, true},
		{RoleViewer, PermPeerCreate, false},
		{RoleViewer, PermServerWrite, false},
		{RoleViewer, PermConfigExport, false},
	}

	for _, c := range cases {
		if got := c.role.Has(c.perm); got != c.allow {
			t.Errorf("role %s has %s = %v, want %v", c.role, c.perm, got, c.allow)
		}
	}
}

func TestEveryRoleHasPermissions(t *testing.T) {
	for _, role := range AllRoles() {
		if !role.Valid() {
			t.Errorf("%s reported invalid", role)
		}
		if len(role.Permissions()) == 0 {
			t.Errorf("role %s has no permissions", role)
		}
	}
}

func TestInvalidRoleHasNoPermissions(t *testing.T) {
	var bad Role = "superuser"
	if bad.Valid() {
		t.Fatal("unknown role should be invalid")
	}
	if bad.Has(PermServerRead) {
		t.Fatal("unknown role must not grant permissions")
	}
}

func TestPermissionsAreUnique(t *testing.T) {
	for _, role := range AllRoles() {
		seen := map[Permission]bool{}
		for _, p := range role.Permissions() {
			if seen[p] {
				t.Errorf("role %s lists %s twice", role, p)
			}
			seen[p] = true
		}
	}
}
