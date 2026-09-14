//go:build darwin

package terminal

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

func killProcessTree(rootPID int) error {
	root, err := readDarwinProcess(rootPID)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := signalDarwinProcess(root, syscall.SIGSTOP); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	tracked := map[int]unix.KinfoProc{rootPID: root}
	for {
		all, err := unix.SysctlKinfoProcSlice("kern.proc.all")
		if err != nil {
			return errors.Join(err, killTrackedDarwinProcesses(tracked, rootPID))
		}
		children := make(map[int][]unix.KinfoProc)
		for _, process := range all {
			children[int(process.Eproc.Ppid)] = append(children[int(process.Eproc.Ppid)], process)
		}
		queue := []int{rootPID}
		seen := map[int]bool{rootPID: true}
		added := 0
		for len(queue) > 0 {
			parent := queue[0]
			queue = queue[1:]
			for _, process := range children[parent] {
				pid := int(process.Proc.P_pid)
				if seen[pid] {
					continue
				}
				seen[pid] = true
				queue = append(queue, pid)
				if known, ok := tracked[pid]; ok && known.Proc.P_starttime == process.Proc.P_starttime {
					continue
				}
				if err := signalDarwinProcess(process, syscall.SIGSTOP); err != nil {
					if errors.Is(err, syscall.ESRCH) {
						continue
					}
					return errors.Join(err, killTrackedDarwinProcesses(tracked, rootPID))
				}
				tracked[pid] = process
				added++
			}
		}
		if added == 0 {
			break
		}
	}
	return killTrackedDarwinProcesses(tracked, rootPID)
}

func readDarwinProcess(pid int) (unix.KinfoProc, error) {
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return unix.KinfoProc{}, syscall.ESRCH
		}
		return unix.KinfoProc{}, err
	}
	if int(process.Proc.P_pid) != pid {
		return unix.KinfoProc{}, syscall.ESRCH
	}
	return *process, nil
}

// Check birth time before signaling each saved PID so PID reuse never selects
// an unrelated process during descendant-tree cleanup.
func signalDarwinProcess(process unix.KinfoProc, sig syscall.Signal) error {
	pid := int(process.Proc.P_pid)
	current, err := readDarwinProcess(pid)
	if err != nil {
		return err
	}
	if current.Proc.P_starttime != process.Proc.P_starttime {
		return syscall.ESRCH
	}
	return syscall.Kill(pid, sig)
}

func killTrackedDarwinProcesses(processes map[int]unix.KinfoProc, rootPID int) error {
	var errs []error
	kill := func(process unix.KinfoProc) {
		if err := signalDarwinProcess(process, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			errs = append(errs, err)
		}
	}
	for pid, process := range processes {
		if pid != rootPID {
			kill(process)
		}
	}
	if root, ok := processes[rootPID]; ok {
		kill(root)
	}
	return errors.Join(errs...)
}
