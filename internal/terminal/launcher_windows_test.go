//go:build windows

package terminal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestDefaultShellUsesPowerShellOnWindows(t *testing.T) {
	if got := defaultShell(); got != "powershell.exe" {
		t.Fatalf("defaultShell() = %q, want powershell.exe", got)
	}
}

func TestNormalizeConPTYInputConvertsLineFeedsToEnter(t *testing.T) {
	tests := map[string]string{
		"one\ntwo\n":  "one\rtwo\r",
		"one\r\ntwo":  "one\rtwo",
		"already\r":   "already\r",
		"control\x03": "control\x03",
	}
	for input, want := range tests {
		if got := string(normalizeConPTYInput([]byte(input))); got != want {
			t.Fatalf("normalize %q = %q, want %q", input, got, want)
		}
	}
}

func TestMergeWindowsEnvOverridesKeysCaseInsensitively(t *testing.T) {
	merged := mergeWindowsEnv(
		[]string{"Path=C:\\Windows", "TEMP=C:\\Old", "=C:=C:\\work"},
		map[string]string{"PATH": "C:\\Tools", "NewValue": "kept"},
	)
	want := []string{"=C:=C:\\work", "NewValue=kept", "PATH=C:\\Tools", "TEMP=C:\\Old"}
	if !reflect.DeepEqual(merged, want) {
		t.Fatalf("merged environment = %#v, want %#v", merged, want)
	}
}

func TestConPTYJobTerminationIsConcurrentAndIdempotent(t *testing.T) {
	wantErr := errors.New("job release failed")
	var calls atomic.Int32
	process := &conPTYProcess{
		job: windows.Handle(42),
		releaseJob: func(job windows.Handle) error {
			if job != windows.Handle(42) {
				t.Fatalf("release job handle = %v, want 42", job)
			}
			calls.Add(1)
			return wantErr
		},
	}

	const callers = 32
	start := make(chan struct{})
	errorsSeen := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errorsSeen <- process.terminateJob()
		}()
	}
	close(start)
	wait.Wait()
	close(errorsSeen)

	if got := calls.Load(); got != 1 {
		t.Fatalf("job release calls = %d, want 1", got)
	}
	for err := range errorsSeen {
		if !errors.Is(err, wantErr) {
			t.Fatalf("terminateJob error = %v, want %v", err, wantErr)
		}
	}
}

func TestEnableInheritedCtrlCHandlingWithoutParentConsole(t *testing.T) {
	if os.Getenv("EXECUTOR_TEST_WITHOUT_CONSOLE") == "1" {
		if err := enableInheritedCtrlCHandling(); err != nil {
			t.Fatalf("enable Ctrl+C handling without parent console: %v", err)
		}
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	command := exec.Command(executable, "-test.run=^TestEnableInheritedCtrlCHandlingWithoutParentConsole$")
	command.Env = append(os.Environ(), "EXECUTOR_TEST_WITHOUT_CONSOLE=1")
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("detached helper failed: %v\n%s", err, output)
	}
}

func TestManagerUsesConPTYWithPersistentStateAndResize(t *testing.T) {
	if result, _, callErr := setConsoleCtrlHandler.Call(0, 1); result == 0 {
		t.Fatalf("disable inherited Ctrl+C handling: %v", callErr)
	}
	t.Cleanup(func() { _, _, _ = setConsoleCtrlHandler.Call(0, 0) })

	manager := NewManager()
	dir := t.TempDir()
	session, err := manager.Start(context.Background(), SessionSpec{
		Dir: dir,
		Env: map[string]string{
			"EXECUTOR_CONPTY_TEST": "kept",
			"path":                 os.Getenv("PATH") + ";C:\\ExecutorOverride",
		},
		Columns: 80,
		Rows:    25,
	})
	if err != nil {
		t.Fatalf("start ConPTY session: %v", err)
	}
	t.Cleanup(func() { _ = manager.Kill(session.ID) })

	if err := manager.Write(session.ID, []byte("Write-Output \"ENV:$env:EXECUTOR_CONPTY_TEST\"; Write-Output \"PATH_OVERRIDE:$($env:PATH.EndsWith('C:\\ExecutorOverride'))\"; Write-Output \"CWD:$((Get-Location).Path)\"; Write-Output \"SIZE:$($Host.UI.RawUI.WindowSize.Width)x$($Host.UI.RawUI.WindowSize.Height)\"\n")); err != nil {
		t.Fatalf("write initial command: %v", err)
	}
	initial := waitForWindowsOutput(t, manager, session.ID, 0, "ENV:kept", "PATH_OVERRIDE:True", "SIZE:80x25")
	if !strings.Contains(strings.ToLower(string(initial.Data)), strings.ToLower("CWD:"+dir)) {
		t.Fatalf("ConPTY output missing cwd %q: %q", dir, string(initial.Data))
	}

	if err := manager.Resize(session.ID, 132, 43); err != nil {
		t.Fatalf("resize ConPTY: %v", err)
	}
	if err := manager.Write(session.ID, []byte("Write-Output \"RESIZED:$($Host.UI.RawUI.WindowSize.Width)x$($Host.UI.RawUI.WindowSize.Height)\"\n")); err != nil {
		t.Fatalf("write resize probe: %v", err)
	}
	resized := waitForWindowsOutput(t, manager, session.ID, initial.NextCursor, "RESIZED:132x43")

	if err := manager.Write(session.ID, []byte("function prompt { 'EXECUTOR_READY> ' }\n")); err != nil {
		t.Fatalf("set prompt: %v", err)
	}
	ready := waitForWindowsOutput(t, manager, session.ID, resized.NextCursor, "EXECUTOR_READY> ")
	if err := manager.Write(session.ID, []byte("Start-Sleep -Seconds 30\n")); err != nil {
		t.Fatalf("start foreground command: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := manager.Signal(session.ID, SignalInterrupt); err != nil {
		t.Fatalf("interrupt foreground command: %v", err)
	}
	interrupted := waitForWindowsOutput(t, manager, session.ID, ready.NextCursor, "EXECUTOR_READY> ")
	if err := manager.Write(session.ID, []byte("Write-Output \"INTERRUPT_OK\"\n")); err != nil {
		t.Fatalf("write after interrupt: %v", err)
	}
	waitForWindowsOutput(t, manager, session.ID, interrupted.NextCursor, "INTERRUPT_OK")

	if err := manager.Write(session.ID, []byte("$child = Start-Process powershell.exe -ArgumentList '-NoProfile','-NonInteractive','-Command','Start-Sleep -Seconds 30' -PassThru; Write-Output \"CHILD:$($child.Id)\"\n")); err != nil {
		t.Fatalf("start child process: %v", err)
	}
	childPattern := regexp.MustCompile(`CHILD:(\d+)\r\n`)
	childOutput := waitForWindowsPattern(t, manager, session.ID, interrupted.NextCursor, childPattern)
	match := childPattern.FindStringSubmatch(string(childOutput.Data))
	if len(match) != 2 {
		t.Fatalf("child PID missing from output: %q", string(childOutput.Data))
	}
	childPID, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatalf("parse child PID: %v", err)
	}
	started := time.Now()
	if err := manager.Kill(session.ID); err != nil {
		t.Fatalf("kill ConPTY process tree: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("ConPTY kill took %s", elapsed)
	}
	waitForWindowsProcessExit(t, childPID)
}

func waitForWindowsOutput(t *testing.T, manager *Manager, sessionID string, cursor int64, needles ...string) OutputChunk {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	lastText := ""
	for time.Now().Before(deadline) {
		chunk, err := manager.Read(sessionID, cursor)
		if err != nil {
			t.Fatalf("read ConPTY output: %v", err)
		}
		text := string(chunk.Data)
		lastText = text
		found := true
		for _, needle := range needles {
			if !strings.Contains(text, needle) {
				found = false
				break
			}
		}
		if found {
			return chunk
		}
		if !chunk.Running {
			t.Fatalf("ConPTY session exited while waiting for %v: %q", needles, text)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for ConPTY output %s: %q", fmt.Sprint(needles), lastText)
	return OutputChunk{}
}

func waitForWindowsProcessExit(t *testing.T, pid int) {
	t.Helper()
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if err == windows.ERROR_INVALID_PARAMETER {
			return
		}
		t.Fatalf("open child process %d: %v", pid, err)
	}
	defer windows.CloseHandle(process)
	status, err := windows.WaitForSingleObject(process, 5000)
	if err != nil {
		t.Fatalf("wait for child process %d: %v", pid, err)
	}
	if status != windows.WAIT_OBJECT_0 {
		t.Fatalf("child process %d remained alive, wait status=%d", pid, status)
	}
}

func waitForWindowsPattern(t *testing.T, manager *Manager, sessionID string, cursor int64, pattern *regexp.Regexp) OutputChunk {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	lastText := ""
	for time.Now().Before(deadline) {
		chunk, err := manager.Read(sessionID, cursor)
		if err != nil {
			t.Fatalf("read ConPTY output: %v", err)
		}
		lastText = string(chunk.Data)
		if pattern.Match(chunk.Data) {
			return chunk
		}
		if !chunk.Running {
			t.Fatalf("ConPTY session exited while waiting for %s: %q", pattern, lastText)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for ConPTY output %s: %q", pattern, lastText)
	return OutputChunk{}
}
