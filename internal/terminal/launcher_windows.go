//go:build windows

package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/UserExistsError/conpty"
	"golang.org/x/sys/windows"
)

type windowsLauncher struct{}

var setConsoleCtrlHandler = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleCtrlHandler")

func newPTYLauncher() ptyLauncher {
	return windowsLauncher{}
}

func (windowsLauncher) Start(spec SessionSpec) (terminalProcess, error) {
	if !conpty.IsConPtyAvailable() {
		return nil, errors.New("ConPTY requires Windows 10 version 1809 or Windows Server 2019 or newer")
	}
	columns, rows := spec.Columns, spec.Rows
	if columns < 1 {
		columns = defaultTerminalColumns
	}
	if rows < 1 {
		rows = defaultTerminalRows
	}
	options := []conpty.ConPtyOption{
		conpty.ConPtyDimensions(columns, rows),
		conpty.ConPtyEnv(mergeWindowsEnv(os.Environ(), spec.Env)),
	}
	if spec.Dir != "" {
		options = append(options, conpty.ConPtyWorkDir(spec.Dir))
	}
	if err := enableInheritedCtrlCHandling(); err != nil {
		return nil, fmt.Errorf("enable inherited Ctrl+C handling: %w", err)
	}
	instance, err := conpty.Start(windows.ComposeCommandLine(spec.Command), options...)
	if err != nil {
		return nil, err
	}
	job, err := newKillOnCloseJob(instance.Pid())
	if err != nil {
		terminateProcess(instance.Pid())
		go instance.Close()
		return nil, fmt.Errorf("attach ConPTY process to kill-on-close job: %w", err)
	}
	return &conPTYProcess{
		instance:   instance,
		job:        job,
		releaseJob: terminateAndCloseJob,
		closeDone:  make(chan struct{}),
	}, nil
}

func enableInheritedCtrlCHandling() error {
	result, _, callErr := setConsoleCtrlHandler.Call(0, 0)
	if result != 0 || errors.Is(callErr, windows.ERROR_INVALID_HANDLE) {
		return nil
	}
	return callErr
}

type conPTYProcess struct {
	instance   *conpty.ConPty
	job        windows.Handle
	releaseJob func(windows.Handle) error
	jobOnce    sync.Once
	jobErr     error
	close      sync.Once
	closeDone  chan struct{}
	closeErr   error
}

func (p *conPTYProcess) PID() int                      { return p.instance.Pid() }
func (p *conPTYProcess) Read(data []byte) (int, error) { return p.instance.Read(data) }
func (p *conPTYProcess) Write(data []byte) (int, error) {
	normalized := normalizeConPTYInput(data)
	written, err := p.instance.Write(normalized)
	if err != nil {
		return 0, err
	}
	if written != len(normalized) {
		return 0, errors.New("short ConPTY input write")
	}
	return len(data), nil
}
func (p *conPTYProcess) Resize(columns, rows int) error { return p.instance.Resize(columns, rows) }
func (p *conPTYProcess) Wait() error {
	code, err := p.instance.Wait(context.Background())
	if err != nil {
		return err
	}
	if code != 0 {
		return processExitError{code: int(code)}
	}
	return nil
}
func (p *conPTYProcess) CloseInput() error { return p.Kill() }
func (p *conPTYProcess) Kill() error {
	return errors.Join(p.terminateJob(), p.closeProcess())
}
func (p *conPTYProcess) Signal(signal Signal) error {
	if signal == SignalInterrupt {
		_, err := p.Write([]byte{3})
		return err
	}
	return p.Kill()
}
func (p *conPTYProcess) closeProcess() error {
	p.close.Do(func() {
		go func() {
			p.closeErr = p.instance.Close()
			close(p.closeDone)
		}()
	})
	select {
	case <-p.closeDone:
		return p.closeErr
	case <-time.After(2 * time.Second):
		return errors.New("timed out cleaning up ConPTY handles after process-tree termination")
	}
}

func (p *conPTYProcess) terminateJob() error {
	p.jobOnce.Do(func() {
		p.jobErr = p.releaseJob(p.job)
	})
	return p.jobErr
}

func terminateAndCloseJob(job windows.Handle) error {
	return errors.Join(
		windows.TerminateJobObject(job, 1),
		windows.CloseHandle(job),
	)
}

func newKillOnCloseJob(pid int) (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func terminateProcess(pid int) {
	process, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(process)
	_ = windows.TerminateProcess(process, 1)
}

func normalizeConPTYInput(input []byte) []byte {
	if !bytes.Contains(input, []byte{'\n'}) {
		return input
	}
	normalized := make([]byte, 0, len(input))
	for index, value := range input {
		if value == '\n' {
			if index == 0 || input[index-1] != '\r' {
				normalized = append(normalized, '\r')
			}
			continue
		}
		normalized = append(normalized, value)
	}
	return normalized
}

func mergeWindowsEnv(base []string, overrides map[string]string) []string {
	merged := make(map[string]string, len(base)+len(overrides))
	for _, entry := range base {
		merged[strings.ToUpper(windowsEnvKey(entry))] = entry
	}
	for key, value := range overrides {
		merged[strings.ToUpper(key)] = key + "=" + value
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, merged[key])
	}
	return result
}

func windowsEnvKey(entry string) string {
	start := 0
	if strings.HasPrefix(entry, "=") {
		start = 1
	}
	if separator := strings.IndexByte(entry[start:], '='); separator >= 0 {
		return entry[:start+separator]
	}
	return entry
}
