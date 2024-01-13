package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/losfair/hgw/homegw-libs/kexec"
	"go.uber.org/zap"
)

func emergencyKexecEntry() {
	logger, err := zap.NewProductionConfig().Build()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}

	key, err := os.ReadFile("/kexec-encryption-key.txt")
	if err != nil {
		logger.Fatal("can't read kexec encryption key", zap.Error(err))
	}

	decodedKey, err := base64.StdEncoding.DecodeString(string(key))
	if err != nil {
		logger.Fatal("can't decode kexec encryption key", zap.Error(err))
	}
	if len(decodedKey) != 32 {
		logger.Fatal("kexec encryption key is not 32 bytes long")
	}
	var decodedKeyArray [32]byte
	copy(decodedKeyArray[:], decodedKey)

	logger.Info("emergency-kexec started")

	http.HandleFunc("/emergency-kexec", func(w http.ResponseWriter, r *http.Request) {
		if !globalKexecLock.TryLock() {
			w.WriteHeader(429)
			return
		}

		defer globalKexecLock.Unlock()

		if r.ContentLength > 128*1048576 {
			w.WriteHeader(400)
			w.Write([]byte("kexec package is too big"))
			return
		}

		input := make([]byte, r.ContentLength)
		_, err := io.ReadFull(r.Body, input)
		if err != nil {
			w.WriteHeader(400)
			return
		}

		pkg, err := kexec.Unseal(logger, 1, decodedKeyArray, input, r.Header.Get("X-External-Signature"))
		if err != nil {
			logger.Error("failed to unseal kexec package", zap.Error(err))
			w.WriteHeader(400)
			w.Write([]byte(fmt.Sprintf("unseal failed: %v", err)))
			return
		}

		// Kill user processes
		// Two continuous pkills to work around race condition
		for {
			cmd := exec.Command("pkill", "-SIGKILL", "-U", "1000")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if cmd.Run() != nil {
				cmd = exec.Command("pkill", "-SIGKILL", "-U", "1000")
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				if cmd.Run() != nil {
					logger.Info("killed all user processes")
					break
				}
			}
		}

		// Unmount user tmp
		err = syscall.Unmount("/vroot/tmp", 0)
		if err != nil {
			logger.Error("failed to unmount /vroot/tmp", zap.Error(err))
		}

		// Save config for the new kernel
		if err := writeConfigToOcram(pkg.Config); err != nil {
			logger.Error("failed to write config to ocram", zap.Error(err))
			w.WriteHeader(500)
			return
		}

		kexecSegment := make([]uintptr, 4)
		kexecSegment[0] = uintptr(unsafe.Pointer(&pkg.Kernel[0]))
		kexecSegment[1] = uintptr(len(pkg.Kernel))
		kexecSegment[2] = 0x80000000
		kexecSegment[3] = 0x8000000

		_, _, errno := syscall.Syscall6(syscall.SYS_KEXEC_LOAD, 0x80000000, 1, uintptr(unsafe.Pointer(&kexecSegment[0])), 0, 0, 0)
		runtime.KeepAlive(kexecSegment)
		runtime.KeepAlive(pkg)

		if errno != 0 {
			logger.Error("failed to load kexec package", zap.Error(errno))
			w.WriteHeader(500)
			return
		}

		logger.Info("kexec image loaded, rebooting")
		logger.Sync()

		_, _, errno = syscall.Syscall6(syscall.SYS_REBOOT, syscall.LINUX_REBOOT_MAGIC1, syscall.LINUX_REBOOT_MAGIC2, syscall.LINUX_REBOOT_CMD_KEXEC, 0, 0, 0)
		if errno != 0 {
			logger.Fatal("failed to reboot", zap.Error(errno))
		}
	})

	http.ListenAndServe(":2345", nil)
}
