package daemon

import "context"

type hookPeerPIDKey struct{}

func withHookPeerPID(ctx context.Context, pid int) context.Context {
	if pid <= 0 {
		return ctx
	}
	return context.WithValue(ctx, hookPeerPIDKey{}, pid)
}

func hookPeerPID(ctx context.Context) int {
	pid, _ := ctx.Value(hookPeerPIDKey{}).(int)
	return pid
}

func (d *Daemon) pidParent(pid int) (int, bool) {
	if d != nil && d.parentPID != nil {
		return d.parentPID(pid)
	}
	return osParentPID(pid)
}

func isDescendantPID(pid, ancestor int, parentOf func(int) (int, bool)) bool {
	if pid <= 0 || ancestor <= 0 {
		return false
	}
	seen := map[int]bool{}
	cur := pid
	for i := 0; i < 64; i++ {
		if cur == ancestor {
			return true
		}
		if cur <= 1 || seen[cur] {
			return false
		}
		seen[cur] = true
		ppid, ok := parentOf(cur)
		if !ok || ppid <= 0 || ppid == cur {
			return false
		}
		cur = ppid
	}
	return false
}
