package sandbox

import "sync"

// Leases protect a root execution tree from normal local Docker reaping.
// They do not override an explicit Close or a cloud provider's hard TTL.
var workspaceLeases = struct {
	sync.Mutex
	refs map[string]int
}{refs: make(map[string]int)}

func AcquireLease(workspaceID string) func() {
	workspaceLeases.Lock()
	workspaceLeases.refs[workspaceID]++
	workspaceLeases.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			workspaceLeases.Lock()
			defer workspaceLeases.Unlock()
			workspaceLeases.refs[workspaceID]--
			if workspaceLeases.refs[workspaceID] <= 0 {
				delete(workspaceLeases.refs, workspaceID)
			}
		})
	}
}

func workspaceLeased(workspaceID string) bool {
	workspaceLeases.Lock()
	defer workspaceLeases.Unlock()
	return workspaceLeases.refs[workspaceID] > 0
}
