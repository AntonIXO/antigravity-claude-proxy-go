package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// PIDFilePath returns the path to ~/.config/antigravity-proxy/server.pid.
func PIDFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "antigravity-proxy", "server.pid"), nil
}

// LogFilePath returns the path to ~/.config/antigravity-proxy/server.log.
func LogFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "antigravity-proxy", "server.log"), nil
}

// WritePIDFile writes the current PID to server.pid.
func WritePIDFile(pid int) error {
	path, err := PIDFilePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(pid)), 0644)
}

// ReadPIDFile reads the PID from server.pid.
func ReadPIDFile() (int, error) {
	path, err := PIDFilePath()
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("invalid PID file: %w", err)
	}
	return pid, nil
}

// RemovePIDFile deletes the server.pid file.
func RemovePIDFile() {
	if path, err := PIDFilePath(); err == nil {
		_ = os.Remove(path)
	}
}

// IsProcessRunning checks if a process with given PID is alive.
func IsProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 tests process existence without sending an actual signal
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// StopDaemon terminates the running daemon process.
func StopDaemon() error {
	pid, err := ReadPIDFile()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("no running proxy server found (PID file missing)")
		}
		return err
	}

	if !IsProcessRunning(pid) {
		RemovePIDFile()
		return fmt.Errorf("process %d was not running (cleaned up stale PID file)", pid)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		RemovePIDFile()
		return err
	}

	// Send SIGTERM
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("failed to signal process %d: %w", pid, err)
	}

	// Wait up to 5 seconds for process to exit
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !IsProcessRunning(pid) {
			RemovePIDFile()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Force kill if still running
	_ = proc.Kill()
	RemovePIDFile()
	return nil
}

// StartDaemon spawns the current executable in the background.
func StartDaemon(args []string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("find executable: %w", err)
	}

	logPath, err := LogFilePath()
	if err != nil {
		return 0, err
	}
	_ = os.MkdirAll(filepath.Dir(logPath), 0755)

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return 0, fmt.Errorf("open log file: %w", err)
	}

	// Filter out --daemon from args to prevent recursive spawning
	childArgs := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--daemon" || arg == "-daemon" || strings.HasPrefix(arg, "--daemon=") {
			continue
		}
		childArgs = append(childArgs, arg)
	}

	cmd := exec.Command(exe, childArgs...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return 0, fmt.Errorf("spawn background process: %w", err)
	}

	pid := cmd.Process.Pid
	if err := WritePIDFile(pid); err != nil {
		_ = cmd.Process.Kill()
		logFile.Close()
		return 0, fmt.Errorf("write PID file: %w", err)
	}

	return pid, nil
}
