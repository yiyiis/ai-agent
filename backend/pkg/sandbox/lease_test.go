package sandbox

import "testing"

func TestWorkspaceLeaseReferenceCounting(t *testing.T) {
	a, b := AcquireLease("lease-test"), AcquireLease("lease-test")
	if !workspaceLeased("lease-test") {
		t.Fatal("missing active lease")
	}
	a()
	a()
	if !workspaceLeased("lease-test") {
		t.Fatal("double release removed another owner")
	}
	b()
	if workspaceLeased("lease-test") {
		t.Fatal("lease leaked after last owner")
	}
}
