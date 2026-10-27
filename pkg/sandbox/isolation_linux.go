//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Landlock is the enforcement mechanism: an unprivileged process can restrict
// its own filesystem and TCP access, the restriction survives execve, and it
// needs no capabilities, namespaces or helper binary. That is why mode
// isolation is part of golem rather than a wrapper around bubblewrap.
//
// Three kernel versions matter: ABI 1 confines the filesystem, ABI 4 adds TCP
// bind/connect, ABI 6 adds a scope that stops signals and abstract-socket
// connections from crossing the boundary. Older kernels still get filesystem
// confinement, and IsolationFeatures says exactly what is enforced.

// Landlock access rights, from include/uapi/linux/landlock.h.
const (
	llAccessFSExecute    uint64 = 1 << 0
	llAccessFSWriteFile  uint64 = 1 << 1
	llAccessFSReadFile   uint64 = 1 << 2
	llAccessFSReadDir    uint64 = 1 << 3
	llAccessFSRemoveDir  uint64 = 1 << 4
	llAccessFSRemoveFile uint64 = 1 << 5
	llAccessFSMakeChar   uint64 = 1 << 6
	llAccessFSMakeDir    uint64 = 1 << 7
	llAccessFSMakeReg    uint64 = 1 << 8
	llAccessFSMakeSock   uint64 = 1 << 9
	llAccessFSMakeFifo   uint64 = 1 << 10
	llAccessFSMakeBlock  uint64 = 1 << 11
	llAccessFSMakeSym    uint64 = 1 << 12
	llAccessFSRefer      uint64 = 1 << 13
	llAccessFSTruncate   uint64 = 1 << 14
	llAccessFSIoctlDev   uint64 = 1 << 15

	llAccessNetBindTCP    uint64 = 1 << 0
	llAccessNetConnectTCP uint64 = 1 << 1

	llScopeAbstractUnixSocket uint64 = 1 << 0
	llScopeSignal             uint64 = 1 << 1
)

// llRulePathBeneath is LANDLOCK_RULE_PATH_BENEATH.
const llRulePathBeneath = 1

// llCreateRulesetVersion asks the kernel for its ABI version instead of an fd.
const llCreateRulesetVersion = 1 << 0

type llRulesetAttr struct {
	handledAccessFS  uint64
	handledAccessNet uint64
	scoped           uint64
}

type llPathBeneathAttr struct {
	allowedAccess uint64
	parentFD      int32
	_             uint32
}

// init turns this process into the sandbox wrapper when the parent asked for
// one. It lives in init rather than main so that the test binary is the same
// wrapper the agent uses, and so no flag parsing happens first.
func init() {
	raw := os.Getenv(envIsolationSpec)
	if raw == "" {
		return
	}
	// The confined command must not see the spec, and must not be able to
	// re-enter the wrapper by accident.
	os.Unsetenv(envIsolationSpec)
	runConfinedChild(raw) // never returns
}

var (
	landlockOnce sync.Once
	landlockABI  int
)

// LandlockABI reports the kernel's Landlock ABI version, or 0 when unsupported.
func LandlockABI() int {
	landlockOnce.Do(func() {
		version, _, errno := syscall.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, llCreateRulesetVersion)
		if errno != 0 {
			landlockABI = 0
			return
		}
		landlockABI = int(version)
	})
	return landlockABI
}

// IsolationSupported reports whether this host can enforce mode isolation.
func IsolationSupported() error {
	if LandlockABI() < 1 {
		return fmt.Errorf("%w: the kernel has no Landlock support (needs Linux 5.13+)", ErrIsolationUnsupported)
	}
	return nil
}

// IsolationFeatures describes what the kernel will actually enforce.
func IsolationFeatures() string {
	abi := LandlockABI()
	switch {
	case abi <= 0:
		return "unavailable"
	case abi < 4:
		return fmt.Sprintf("Landlock ABI %d, filesystem only (network not enforced)", abi)
	case abi < 6:
		return fmt.Sprintf("Landlock ABI %d, filesystem + TCP", abi)
	default:
		return fmt.Sprintf("Landlock ABI %d, filesystem + TCP + signal scope", abi)
	}
}

// runConfinedChild is the wrapper: confine this process, then become the
// command. Every failure exits with isolationExitSetupFailure so the parent
// reports a sandbox problem instead of a command result.
func runConfinedChild(raw string) {
	spec, err := decodeSpec(raw)
	if err != nil {
		wrapperFatal(err)
	}
	// Landlock domains are per-thread until execve collapses the process, so
	// pin the goroutine: execing from an unrestricted thread would escape.
	runtime.LockOSThread()

	if err := applyRlimits(); err != nil {
		wrapperFatal(err)
	}
	if spec.Dir != "" {
		if err := os.Chdir(spec.Dir); err != nil {
			wrapperFatal(fmt.Errorf("chdir %s: %w", spec.Dir, err))
		}
	}
	// Resolve bash before restricting: PATH lookups stat directories that the
	// ruleset may not cover.
	shell, err := exec.LookPath("bash")
	if err != nil {
		wrapperFatal(fmt.Errorf("finding bash: %w", err))
	}
	if err := restrictSelf(spec); err != nil {
		wrapperFatal(err)
	}
	if err := syscall.Exec(shell, []string{"bash", "-c", spec.Command}, spec.Env); err != nil {
		wrapperFatal(fmt.Errorf("exec %s: %w", shell, err))
	}
}

func wrapperFatal(err error) {
	fmt.Fprintf(os.Stderr, "golem sandbox: %v\n", err)
	os.Exit(isolationExitSetupFailure)
}

// applyRlimits drops the capability that only produces damage: a crashed tool
// cannot dump core over the workspace.
func applyRlimits() error {
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0}); err != nil {
		return fmt.Errorf("setting RLIMIT_CORE: %w", err)
	}
	return nil
}

// restrictSelf builds the ruleset, installs it and locks it in. After this the
// process can only reach the paths in spec, and so can everything it execs.
func restrictSelf(spec Spec) error {
	abi := LandlockABI()
	if abi < 1 {
		return fmt.Errorf("%w: the kernel has no Landlock support", ErrIsolationUnsupported)
	}

	handledFS := uint64(llAccessFSExecute | llAccessFSWriteFile | llAccessFSReadFile | llAccessFSReadDir |
		llAccessFSRemoveDir | llAccessFSRemoveFile | llAccessFSMakeChar | llAccessFSMakeDir |
		llAccessFSMakeReg | llAccessFSMakeSock | llAccessFSMakeFifo | llAccessFSMakeBlock | llAccessFSMakeSym)
	if abi >= 2 {
		handledFS |= llAccessFSRefer
	}
	if abi >= 3 {
		handledFS |= llAccessFSTruncate
	}
	if abi >= 5 {
		handledFS |= llAccessFSIoctlDev
	}

	attr := llRulesetAttr{handledAccessFS: handledFS}
	size := unsafe.Sizeof(attr.handledAccessFS)
	if abi >= 4 && !spec.AllowNet {
		// Handling the network without granting a rule refuses every TCP bind
		// and connect. Leaving it unhandled is how "network allowed" is
		// expressed: Landlock only has allow rules, so a permissive ruleset is
		// one that does not mention the network at all.
		attr.handledAccessNet = llAccessNetBindTCP | llAccessNetConnectTCP
		size = unsafe.Offsetof(attr.handledAccessNet) + unsafe.Sizeof(attr.handledAccessNet)
	}
	if abi >= 6 {
		// Abstract unix sockets are how a confined process would otherwise
		// reach a desktop session bus or an X server.
		attr.scoped = llScopeAbstractUnixSocket
		if spec.ScopeSignal {
			attr.scoped |= llScopeSignal
		}
		size = unsafe.Sizeof(attr)
	}

	rulesetFD, _, errno := syscall.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), size, 0)
	if errno != 0 {
		return fmt.Errorf("creating landlock ruleset: %w", errno)
	}
	defer syscall.Close(int(rulesetFD))

	for _, path := range spec.ReadOnly {
		if err := addPathRule(int(rulesetFD), path, handledFS, false); err != nil {
			return err
		}
	}
	for _, path := range spec.ReadWrite {
		if err := addPathRule(int(rulesetFD), path, handledFS, true); err != nil {
			return err
		}
	}

	// Required before landlock_restrict_self, and independently useful: the
	// command cannot gain privilege through a setuid binary.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("setting no_new_privs: %w", err)
	}
	if _, _, errno := syscall.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, rulesetFD, 0, 0); errno != 0 {
		return fmt.Errorf("applying landlock ruleset: %w", errno)
	}
	return nil
}

// addPathRule grants access beneath one path. Directories take the directory
// rights; a plain file (a device node, ~/.gitconfig) may only be given the
// rights the kernel allows on files, otherwise the rule is rejected outright.
func addPathRule(rulesetFD int, path string, handledFS uint64, writable bool) error {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		// A path that vanished between listing and opening is not a sandbox
		// failure: the command simply cannot reach it, which is the default.
		return nil
	}
	defer unix.Close(fd)

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("inspecting %s: %w", path, err)
	}

	var allowed uint64
	if st.Mode&unix.S_IFMT == unix.S_IFDIR {
		allowed = llAccessFSExecute | llAccessFSReadFile | llAccessFSReadDir
		if writable {
			allowed = handledFS
		}
	} else {
		allowed = llAccessFSExecute | llAccessFSReadFile
		if handledFS&llAccessFSTruncate != 0 {
			allowed |= llAccessFSTruncate
		}
		if handledFS&llAccessFSIoctlDev != 0 {
			allowed |= llAccessFSIoctlDev
		}
		if writable {
			allowed |= llAccessFSWriteFile
		}
	}

	rule := llPathBeneathAttr{allowedAccess: allowed, parentFD: int32(fd)}
	if _, _, errno := syscall.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(rulesetFD), llRulePathBeneath, uintptr(unsafe.Pointer(&rule)), 0, 0, 0); errno != 0 {
		return fmt.Errorf("granting %s: %w", path, errno)
	}
	return nil
}
