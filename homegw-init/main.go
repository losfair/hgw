package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	kafka_sink "github.com/losfair/hgw/homegw-libs/kafka-sink"
	rt_control "github.com/losfair/hgw/homegw-libs/rt-control"
	tesla_api "github.com/losfair/hgw/homegw-libs/tesla-api"
	"go.uber.org/zap"
)

var GitCommit string
var GithubRunId string

func main() {
	if len(os.Args) == 2 && os.Args[1] == "emergency-kexec" {
		emergencyKexecEntry()
		return
	}

	config, configText := loadConfig()

	// Prepare for emergency kexec
	os.WriteFile("/kexec-encryption-key.txt", []byte(config.KexecEncryptionKey), 0600)

	logger := setupLogging(config.LogKafkaUrl).With(zap.String("hostname", config.Hostname))
	defer logger.Sync()

	disableCoreDump(logger)
	rt_control.NewRtControl(logger, configText)

	selfHash := computeSelfHash(logger)

	logger.Info("homegw-init started", zap.String("git_commit", GitCommit), zap.String("github_run_id", GithubRunId), zap.String("self_hash", hex.EncodeToString(selfHash[:])))

	err := syscall.Sethostname([]byte(config.Hostname))
	if err != nil {
		logger.Error("failed to set hostname", zap.Error(err))
	}

	go relayKernelLogs(logger)

	var teslaApi []*tesla_api.VehicleApi

	for _, vehicle := range config.TeslaApi {
		logger := logger.With(zap.String("component", "tesla-api"), zap.String("vin", vehicle.Vin))
		v, err := tesla_api.NewVehicleApi(context.Background(), logger, &vehicle)
		if err != nil {
			logger.Error("failed to initialize tesla api", zap.Error(err))
		}
		teslaApi = append(teslaApi, v)
	}

	for _, wg := range config.Wireguard {
		logger := logger.With(zap.String("component", "wireguard"), zap.String("interface", wg.Interface))
		err := wg.Apply(logger)
		if err != nil {
			logger.Error("failed to apply wireguard config", zap.Error(err))
		}
	}

	for _, netif := range config.Netif {
		logger := logger.With(zap.String("component", "netif"), zap.String("netif_name", netif.Name))
		err := netif.Start(logger)
		if err != nil {
			logger.Error("failed to start netif", zap.Error(err))
		}
	}

	for _, perm := range config.FsPermissions {
		logger := logger.With(zap.String("component", "fs-permission"), zap.String("fsperm_path", perm.Path), zap.String("fsperm_name", perm.Name), zap.String("fsperm_type", perm.Type), zap.String("chmod", perm.Chmod), zap.String("chown", perm.Chown))
		perm.Apply(logger)
	}

	for _, sysctl := range config.Sysctl {
		logger := logger.With(zap.String("component", "sysctl"), zap.String("sysctl_name", sysctl.Name), zap.String("sysctl_value", sysctl.Value))
		sysctl.Apply(logger)
	}

	diskOpened := make(chan struct{})

	go func() {
		defer close(diskOpened)

		for _, disk := range config.Disks {
			logger := logger.With(zap.String("component", "disk"), zap.String("device", disk.Device), zap.String("encrypted_device", disk.EncryptedDevice), zap.String("mountpoint", disk.Mountpoint))
			err := disk.Open(logger)
			if err != nil {
				logger.Error("failed to open disk", zap.Error(err))
			}
		}

		logger.Info("all disks opened")
	}()

	go func() {
		<-diskOpened
		if config.Netboot != nil {
			logger := logger.With(zap.String("component", "netboot"))
			config.Netboot.Start(logger)
		}
	}()

	sshKill := make(chan struct{})
	sshKillCompletion := make(chan struct{})

	if config.SshServer != nil {
		logger := logger.With(zap.String("component", "ssh-server"))
		err := config.SshServer.Spawn(logger, sshKill, sshKillCompletion)
		if err != nil {
			logger.Error("ssh server spawn failed", zap.Error(err))
		}
	} else {
		close(sshKillCompletion)
	}

	if config.ApiServer != nil {
		gin.SetMode(gin.ReleaseMode)
		logger := logger.With(zap.String("component", "api-server"), zap.String("listen", config.ApiServer.Listen))

		var kexecEncryptionKey [32]byte
		var kexecEnabled bool

		n, err := base64.StdEncoding.Decode(kexecEncryptionKey[:], []byte(config.KexecEncryptionKey))
		if err != nil {
			logger.Error("failed to decode kexec encryption key", zap.Error(err))
		} else if n != 32 {
			logger.Error("kexec encryption key is not 32 bytes long")
		} else {
			kexecEnabled = true
		}

		apiServer := ApiServer{
			Logger:                 logger,
			Version:                config.Version,
			Config:                 config.ApiServer,
			TeslaApi:               teslaApi,
			KexecEncryptionKey:     kexecEncryptionKey,
			KexecEnabled:           kexecEnabled,
			Disks:                  config.Disks,
			KexecSshKill:           sshKill,
			KexecSshKillCompletion: sshKillCompletion,
		}
		go func() {
			err := apiServer.Run()
			if err != nil {
				logger.Error("api server failed", zap.Error(err))
			}
		}()
	}

	logger.Info("initialization completed")
	select {}
}

func relayKernelLogs(logger *zap.Logger) {
	// Don't start streaming until the remote end is ready to receive logs
	for kafka_sink.TotalCompletedMessages.Load() == 0 {
		time.Sleep(1 * time.Second)
	}

	kernelLog, err := os.Open("/dev/kmsg")
	if err != nil {
		logger.Info("can't open /dev/kmsg, not relaying kernel logs", zap.Error(err))
		return
	}

	defer kernelLog.Close()

	scanner := bufio.NewScanner(kernelLog)
	for scanner.Scan() {
		logger.Info("kernel message", zap.String("kmsg", scanner.Text()))
	}
}

func disableCoreDump(logger *zap.Logger) {
	_, _, errno := syscall.Syscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 0, 0)
	if errno != 0 {
		logger.Fatal("can't disable core dumps", zap.Error(errno))
	}
	logger.Info("core dumps disabled")

}

func computeSelfHash(logger *zap.Logger) [32]byte {
	exe, err := os.OpenFile("/proc/self/exe", os.O_RDONLY, 0)
	if err != nil {
		logger.Fatal("failed to open /proc/self/exe", zap.Error(err))
	}
	defer exe.Close()

	stat, err := exe.Stat()
	if err != nil {
		logger.Fatal("failed to stat /proc/self/exe", zap.Error(err))
	}
	m, err := syscall.Mmap(int(exe.Fd()), 0, int(stat.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		logger.Fatal("failed to mmap /proc/self/exe", zap.Error(err))
	}
	defer syscall.Munmap(m)

	return sha256.Sum256(m)
}
