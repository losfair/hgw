package main

import (
	"os"
	"os/exec"

	"github.com/losfair/hgw/homegw-libs/disk"
	"github.com/losfair/hgw/homegw-libs/netboot"
	tesla_api "github.com/losfair/hgw/homegw-libs/tesla-api"
	"go.uber.org/zap"
)

type InitConfig struct {
	Version            int64                      `json:"version"`
	Hostname           string                     `json:"hostname"`
	LogKafkaUrl        string                     `json:"log_kafka_url"`
	TeslaApi           []tesla_api.TeslaApiConfig `json:"tesla_api"`
	Wireguard          []WireguardConfig          `json:"wireguard"`
	ApiServer          *ApiServerConfig           `json:"api_server"`
	SshServer          *SshServerConfig           `json:"ssh_server"`
	KexecEncryptionKey string                     `json:"kexec_encryption_key"`
	Disks              []disk.DiskConfig          `json:"disks"`
	Netif              []NetifConfig              `json:"netif"`
	FsPermissions      []FsPermissionConfig       `json:"fs_permissions"`
	Netboot            *netboot.NetbootConfig     `json:"netboot"`
	Sysctl             []SysctlConfig             `json:"sysctl"`
}

type FsPermissionConfig struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Chmod string `json:"chmod"`
	Chown string `json:"chown"`
}

func (c *FsPermissionConfig) Apply(logger *zap.Logger) {
	doIt := func(opName string, execCmd []string) {
		cmd := exec.Command("find", c.Path)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if c.Name != "" {
			cmd.Args = append(cmd.Args, "-name", c.Name)
		}
		if c.Type != "" {
			cmd.Args = append(cmd.Args, "-type", c.Type)
		}
		cmd.Args = append(cmd.Args, "-exec")
		cmd.Args = append(cmd.Args, execCmd...)
		err := cmd.Run()
		if err != nil {
			logger.Error("failed to apply fs permission", zap.String("op", opName), zap.Error(err))
		}

		logger.Info("applied fs permission", zap.String("op", opName))
	}

	if c.Chmod != "" {
		doIt("chmod", []string{"chmod", c.Chmod, "{}", ";"})
	}

	if c.Chown != "" {
		doIt("chown", []string{"chown", c.Chown, "{}", ";"})
	}
}

type SysctlConfig struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (c *SysctlConfig) Apply(logger *zap.Logger) {
	cmd := exec.Command("sysctl", "-w", c.Name+"="+c.Value)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		logger.Error("failed to apply sysctl", zap.Error(err))
	}

	logger.Info("applied sysctl")
}
