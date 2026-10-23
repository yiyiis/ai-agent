package sandbox

import (
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
)

const remoteCleanupTimeout = 15 * time.Second

// A remote command runs in its own process group. The supervisor and cancellation
// helper lock the same server-generated marker before starting/killing it. A
// cancellation tombstone is retained until the sandbox is destroyed: a delayed
// HTTP start cannot start a command after cancellation has already returned.
// Python3 is already part of both remote drivers' execution contract.
const remoteProcessCommon = `
import fcntl, os, signal, subprocess, sys, time, uuid
token = sys.argv[1]
if str(uuid.UUID(token)) != token:
    raise ValueError("invalid execution identity")
directory = "/tmp/ai-agent-exec-" + token
os.makedirs(directory, mode=0o700, exist_ok=True)
lock = open(directory + "/lock", "a")
cancel_path = directory + "/cancelled"
pid_path = directory + "/pgid"
done_path = directory + "/done"

def live_group(pgid):
    if pgid <= 1 or pgid == os.getpgrp():
        raise RuntimeError("refusing invalid command process group")
    for entry in os.scandir("/proc"):
        if not entry.name.isdigit():
            continue
        try:
            with open(entry.path + "/stat") as source:
                fields = source.read().rsplit(")", 1)[1].split()
            if int(fields[2]) == pgid and fields[0] not in ("Z", "X"):
                return True
        except (FileNotFoundError, ProcessLookupError):
            continue
    return False

def stop_group(pgid):
    if not live_group(pgid):
        return
    try:
        os.killpg(pgid, signal.SIGTERM)
    except ProcessLookupError:
        return
    limit = time.monotonic() + 0.3
    while live_group(pgid) and time.monotonic() < limit:
        time.sleep(0.02)
    if live_group(pgid):
        try:
            os.killpg(pgid, signal.SIGKILL)
        except ProcessLookupError:
            return
    limit = time.monotonic() + 3
    while live_group(pgid) and time.monotonic() < limit:
        time.sleep(0.02)
    if live_group(pgid):
        raise RuntimeError("command process group did not exit")

def finish(cancelled):
    fcntl.flock(lock, fcntl.LOCK_EX)
    try:
        if cancelled:
            open(cancel_path, "a").close()
        if os.path.exists(done_path):
            return
        if os.path.exists(pid_path):
            with open(pid_path) as source:
                pgid = int(source.read())
            stop_group(pgid)
            os.unlink(pid_path)
        open(done_path, "a").close()
    finally:
        fcntl.flock(lock, fcntl.LOCK_UN)
`

const remoteProcessSupervisor = remoteProcessCommon + `
seconds = max(1, int(sys.argv[2]))
# The child must not execute user code until its process group is durably
# registered. If this supervisor dies between Popen and writing pgid, the gate
# obtains the abandoned flock, finds no matching registration, and exits.
gate = r'''
import fcntl, os, sys
directory = "/tmp/ai-agent-exec-" + sys.argv[1]
with open(directory + "/lock", "a") as startup_lock:
    fcntl.flock(startup_lock, fcntl.LOCK_EX)
    if os.path.exists(directory + "/cancelled") or os.path.exists(directory + "/done"):
        sys.exit(125)
    try:
        with open(directory + "/pgid") as source:
            registered = int(source.read())
    except FileNotFoundError:
        sys.exit(125)
    if registered != os.getpid():
        sys.exit(125)
    os.execvpe(sys.argv[2], sys.argv[2:], os.environ)
'''
fcntl.flock(lock, fcntl.LOCK_EX)
try:
    if os.path.exists(cancel_path) or os.path.exists(done_path) or os.path.exists(pid_path):
        sys.exit(125)
    child = subprocess.Popen([sys.executable, "-c", gate, token] + sys.argv[3:], start_new_session=True)
    with open(pid_path, "w") as target:
        target.write(str(child.pid))
finally:
    fcntl.flock(lock, fcntl.LOCK_UN)
try:
    code = child.wait(timeout=seconds)
except subprocess.TimeoutExpired:
    code = 124
finally:
    finish(False)
    child.wait()
sys.exit(code if code >= 0 else 128 - code)
`

const remoteProcessCleanup = remoteProcessCommon + `
finish(True)
`

func supervisedRemoteCommand(python string, timeoutSecs int, argv []string) (string, []string) {
	token := uuid.NewString()
	return token, append([]string{python, "-c", remoteProcessSupervisor, token, strconv.Itoa(timeoutSecs)}, argv...)
}

func cleanupRemoteCommand(python, token string) []string {
	return []string{python, "-c", remoteProcessCleanup, token}
}

func blockedWorkspaceError() error {
	return fmt.Errorf("无法确认上一条远端命令已退出，当前工作区已隔离并停止文件与命令操作；请新建会话或由管理员销毁并重建沙箱")
}
