package shellapp

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/duxlabs-dev/dux-go/log"
)

var sessionTakeoverGrace = 5 * time.Second

// proc is a PID pinned to its start time; start 0 means unrecorded.
type proc struct {
	pid   int
	start uint64
}

// alive is false once the process exits, turns zombie, or its PID is reused.
func (p proc) alive() bool {
	if p.pid <= 0 || !processAlive(p.pid) {
		return false
	}
	state, start, ok := procStat(p.pid)
	if !ok {
		return p.start == 0
	}
	return state != 'Z' && (p.start == 0 || start == p.start)
}

type sessionInstance struct {
	base     string // runtime path without extension
	parent   proc
	ui       proc
	verified bool // start times recorded and matching the live processes
}

func (a *App) currentSessionInstance() (sessionInstance, bool) {
	parentPID, prefix, ok := a.sessionParentPID(os.Getenv("WAYLAND_DISPLAY"))
	if !ok {
		return sessionInstance{}, false
	}

	inst := sessionInstance{
		base:   filepath.Join(a.runtimeDir(), fmt.Sprintf("%s%d", prefix, parentPID)),
		parent: proc{pid: parentPID},
	}
	data, err := os.ReadFile(inst.base + pidFileExtension)
	if err != nil {
		return sessionInstance{}, false
	}
	inst.ui.pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || !processAlive(inst.ui.pid) {
		return sessionInstance{}, false
	}

	// Without a record (older instance, no /proc) the PID is trusted for refusing only.
	if data, err := os.ReadFile(inst.base + startFileExtension); err == nil {
		if _, err := fmt.Sscan(string(data), &inst.parent.start, &inst.ui.start); err == nil {
			if !inst.ui.alive() {
				inst.removeFiles()
				return sessionInstance{}, false
			}
			inst.verified = true
		}
	}

	return inst, true
}

func (s sessionInstance) managed() bool {
	_, err := os.Stat(s.base + managedFileExtension)
	return err == nil
}

// claimSession reports whether this run may start in the current session.
// A service-managed start replaces a verified hand-started instance, since
// refusing exits before readiness and the unit restart-loops.
func (a *App) claimSession(session bool) bool {
	inst, ok := a.currentSessionInstance()
	if !ok {
		return true
	}

	if !session || inst.managed() {
		log.Infof("%s already running for this session (pid=%d); refusing to start a second instance", binaryName(), inst.ui.pid)
		return false
	}

	if !inst.verified {
		log.Warnf("%s instance pid=%d holds this session but cannot be verified; refusing to replace it", binaryName(), inst.ui.pid)
		return false
	}

	log.Infof("replacing hand-started %s instance (pid=%d) with the session-managed one", binaryName(), inst.ui.pid)
	inst.stop(sessionTakeoverGrace)
	return true
}

// stop only signals processes whose start times match the record.
func (s sessionInstance) stop(grace time.Duration) {
	target := s.ui
	if s.parent.alive() {
		target = s.parent
	}
	if target.alive() {
		_ = syscall.Kill(target.pid, syscall.SIGTERM)
	}

	for deadline := time.Now().Add(grace); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if !s.ui.alive() && !target.alive() {
			break
		}
	}

	for _, p := range []proc{target, s.ui} {
		if p.alive() {
			_ = syscall.Kill(p.pid, syscall.SIGKILL)
		}
	}

	s.removeFiles()
}

func (s sessionInstance) removeFiles() {
	for _, ext := range instanceFileExtensions {
		os.Remove(s.base + ext)
	}
}

func procStartTime(pid int) (uint64, bool) {
	_, start, ok := procStat(pid)
	return start, ok
}

func procStat(pid int) (state byte, start uint64, ok bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, false
	}
	i := bytes.LastIndexByte(data, ')')
	if i < 0 {
		return 0, 0, false
	}
	// Fields from state (3) on; starttime is field 22.
	fields := strings.Fields(string(data[i+1:]))
	if len(fields) < 20 {
		return 0, 0, false
	}
	start, err = strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return fields[0][0], start, true
}
