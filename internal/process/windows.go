//go:build windows

package process

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// tree is a job object holding the child. Kill-on-close means the whole tree
// dies with shots even if shots dies abruptly, and terminating the job
// terminates every process the child started.
type tree struct{ job windows.Handle }

func newTree(cmd *exec.Cmd) (*tree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("creating job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("configuring job object: %w", err)
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
	return &tree{job: job}, nil
}

func (t *tree) attach(cmd *exec.Cmd) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("opening child process: %w", err)
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(t.job, h); err != nil {
		return fmt.Errorf("assigning child to job object: %w", err)
	}
	return nil
}

// terminate kills the job. Windows console processes have no graceful
// equivalent of SIGTERM, so there is nothing gentler to try first and [Grace]
// never elapses here.
func (t *tree) terminate() error {
	if err := windows.TerminateJobObject(t.job, 1); err != nil {
		return fmt.Errorf("terminating job object: %w", err)
	}
	return nil
}

func (t *tree) kill() error { return t.terminate() }

func (t *tree) close() {
	if t.job != 0 {
		windows.CloseHandle(t.job)
		t.job = 0
	}
}

// detach clears kill-on-close before releasing the job, because the handle is
// closed either way when shots exits and closing it while the limit stands
// would terminate the very server --leave-server preserves.
func (t *tree) detach() error {
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if _, err := windows.SetInformationJobObject(t.job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return fmt.Errorf("detaching job object: %w", err)
	}
	t.close()
	return nil
}

// shellCmd runs a command line through cmd.exe. The line is placed in the
// command line verbatim: cmd.exe parses its own arguments, and Go's quoting
// rules would mangle anything containing quotes or redirections.
func shellCmd(line string) *exec.Cmd {
	shell := os.Getenv("ComSpec")
	if shell == "" {
		shell = "cmd.exe"
	}
	cmd := exec.Command(shell)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: shell + " /c " + line}
	return cmd
}
