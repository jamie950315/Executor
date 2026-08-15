//go:build linux

package terminal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type linuxProcess struct {
	pid       int
	parentPID int
	state     byte
	startTime string
}

func killProcessTree(rootPID int) error {
	root, err := readLinuxProcess(rootPID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := signalLinuxProcess(root, syscall.SIGSTOP); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}

	tracked := map[int]linuxProcess{root.pid: root}
	for {
		descendants, scanErr := linuxDescendantProcesses(rootPID)
		if scanErr != nil {
			return errors.Join(scanErr, killTrackedLinuxProcesses(tracked, rootPID))
		}

		newProcesses := 0
		for _, process := range descendants {
			if known, ok := tracked[process.pid]; ok && known.startTime == process.startTime {
				continue
			}
			if err := signalLinuxProcess(process, syscall.SIGSTOP); err != nil {
				if !errors.Is(err, syscall.ESRCH) {
					return errors.Join(err, killTrackedLinuxProcesses(tracked, rootPID))
				}
				continue
			}
			tracked[process.pid] = process
			newProcesses++
		}
		if newProcesses == 0 {
			break
		}
	}

	return killTrackedLinuxProcesses(tracked, rootPID)
}

func killTrackedLinuxProcesses(processes map[int]linuxProcess, rootPID int) error {
	var errs []error
	for pid, process := range processes {
		if pid == rootPID {
			continue
		}
		if err := signalLinuxProcess(process, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			errs = append(errs, err)
		}
	}
	if root, ok := processes[rootPID]; ok {
		if err := signalLinuxProcess(root, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func signalLinuxProcess(process linuxProcess, signal syscall.Signal) error {
	current, err := readLinuxProcess(process.pid)
	if errors.Is(err, os.ErrNotExist) {
		return syscall.ESRCH
	}
	if err != nil {
		return err
	}
	if current.startTime != process.startTime {
		return syscall.ESRCH
	}
	return syscall.Kill(process.pid, signal)
}

func linuxDescendantProcesses(rootPID int) ([]linuxProcess, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	children := make(map[int][]linuxProcess)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		process, err := readLinuxProcess(pid)
		if err != nil {
			continue
		}
		children[process.parentPID] = append(children[process.parentPID], process)
	}

	var descendants []linuxProcess
	visited := map[int]bool{rootPID: true}
	var visit func(int)
	visit = func(parent int) {
		for _, child := range children[parent] {
			if visited[child.pid] {
				continue
			}
			visited[child.pid] = true
			visit(child.pid)
			descendants = append(descendants, child)
		}
	}
	visit(rootPID)
	return descendants, nil
}

func readLinuxProcess(pid int) (linuxProcess, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return linuxProcess{}, err
	}
	closing := strings.LastIndexByte(string(data), ')')
	if closing < 0 {
		return linuxProcess{}, fmt.Errorf("invalid /proc/%d/stat", pid)
	}
	fields := strings.Fields(string(data[closing+1:]))
	if len(fields) < 20 || len(fields[0]) != 1 {
		return linuxProcess{}, fmt.Errorf("invalid /proc/%d/stat", pid)
	}
	parentPID, err := strconv.Atoi(fields[1])
	if err != nil {
		return linuxProcess{}, fmt.Errorf("parse /proc/%d/stat parent: %w", pid, err)
	}
	return linuxProcess{
		pid:       pid,
		parentPID: parentPID,
		state:     fields[0][0],
		startTime: fields[19],
	}, nil
}
