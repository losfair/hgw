package main

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/losfair/hgw/homegw-libs/memfd"
	"go.uber.org/zap"
)

type SshServerConfig struct {
	HostKey        string   `json:"host_key"`
	AuthorizedKeys []string `json:"authorized_keys"`
}

func (c *SshServerConfig) Spawn(logger *zap.Logger, kill <-chan struct{}, killCompletion chan<- struct{}) error {
	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("failed to create pipe: %w", err)
	}
	go func() {
		defer pr.Close()

		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			logger.Info("dropbear log", zap.String("line", scanner.Text()))
		}
	}()

	authorizedKeysFile := strings.Join(c.AuthorizedKeys, "\n")
	os.MkdirAll("/vroot/tmp/user/.ssh", 0700)
	os.WriteFile("/vroot/tmp/user/.ssh/authorized_keys", []byte(authorizedKeysFile), 0600)
	os.Chown("/vroot/tmp/user", 1000, 1000)
	os.Chown("/vroot/tmp/user/.ssh", 1000, 1000)
	os.Chown("/vroot/tmp/user/.ssh/authorized_keys", 1000, 1000)

	hostKeyDecoded, err := base64.StdEncoding.DecodeString(c.HostKey)
	if err != nil {
		return fmt.Errorf("failed to decode host key: %v", err)
	}

	hostKey, err := memfd.NewMemfdReadonlyBuffer("host.key", hostKeyDecoded)
	if err != nil {
		return fmt.Errorf("failed to create memfd for host key: %v", err)
	}

	runOnce := func() (*os.Process, error) {
		cmd := exec.Command("dropbear", "-E", "-F", "-s", "-p", "22", "-r", "/proc/self/fd/3")
		cmd.Stdout = pw
		cmd.Stderr = pw
		cmd.SysProcAttr = &syscall.SysProcAttr{Chroot: "/vroot", Ptrace: true, Setpgid: true}
		cmd.ExtraFiles = []*os.File{hostKey}

		runtime.LockOSThread() // for ptrace
		defer runtime.UnlockOSThread()

		err = cmd.Start()
		if err != nil {
			return nil, fmt.Errorf("failed to spawn dropbear: %v", err)
		}

		// wait for stop signal, `.Wait()` cannot be used because it modifies Go process state
		{
			var (
				status syscall.WaitStatus
				rusage syscall.Rusage
				e      error
			)
			for {
				_, e = syscall.Wait4(cmd.Process.Pid, &status, 0, &rusage)
				if e != syscall.EINTR {
					break
				}
			}

			failed := true

			if e != nil {
				logger.Error("failed to wait for dropbear", zap.Error(e))
			} else if !status.Stopped() {
				logger.Error("dropbear is not stopped", zap.Any("status", status))
			} else {
				failed = false
			}

			if failed {
				cmd.Process.Kill()
				return nil, fmt.Errorf("failed to wait for dropbear")
			}
		}

		// Set process parameters
		os.WriteFile(fmt.Sprintf("/proc/%d/oom_score_adj", cmd.Process.Pid), []byte("0"), 0600)
		os.WriteFile(fmt.Sprintf("/proc/%d/limits", cmd.Process.Pid), []byte("Max processes=300:300\n"), 0600)
		syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, 5)

		err = syscall.PtraceDetach(cmd.Process.Pid)
		if err != nil {
			cmd.Process.Kill()
			return nil, fmt.Errorf("failed to detach from dropbear: %v", err)
		}

		logger.Info("dropbear spawned")
		return cmd.Process, nil
	}

	go func() {
		defer close(killCompletion)

		for {
			proc, err := runOnce()
			if err != nil {
				logger.Error("failed to spawn dropbear", zap.Error(err))
				break
			} else {
				stop := make(chan struct{})
				go func() {
					select {
					case <-kill:
						// kill process group
						err := syscall.Kill(-proc.Pid, syscall.SIGKILL)
						if err != nil {
							logger.Error("failed to kill dropbear process group", zap.Error(err))
						} else {
							logger.Info("sent SIGKILL to dropbear process group")
						}
					case <-stop:
					}
				}()
				status, err := proc.Wait()
				close(stop)
				logger.Error("dropbear exited", zap.Error(err), zap.Int("status", status.ExitCode()))
				select {
				case <-kill:
					logger.Info("dropbear killed, not restarting")
					return
				default:
				}
				time.Sleep(1 * time.Second)
			}
		}
	}()

	return nil
}
