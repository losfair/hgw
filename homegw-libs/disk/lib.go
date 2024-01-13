package disk

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"

	"github.com/losfair/hgw/homegw-libs/memfd"
	"go.uber.org/zap"
)

type DiskConfig struct {
	Device          string `json:"device"`
	EncryptedDevice string `json:"encrypted_device"`
	Mountpoint      string `json:"mountpoint"`
	LuksKey         string `json:"luks_key"`
}

func (c *DiskConfig) Open(logger *zap.Logger) error {
	luksKey, err := base64.StdEncoding.DecodeString(c.LuksKey)
	if err != nil {
		return fmt.Errorf("failed to decode luks key: %v", err)
	}

	keyfd, err := memfd.NewMemfdReadonlyBuffer("luks-key", luksKey)
	if err != nil {
		return fmt.Errorf("failed to create memfd for luks key: %v", err)
	}
	defer keyfd.Close()

	cmd := exec.Command("nice", "-n", "10", "cryptsetup", "luksOpen", c.Device, c.EncryptedDevice, "--key-file", "/proc/self/fd/3")
	cmd.ExtraFiles = []*os.File{keyfd}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("failed to open luks device: %v", err)
	}

	os.MkdirAll(c.Mountpoint, 0755)

	cmd = exec.Command("mount", "-t", "ext4", "-o", "nosuid,nodev,noatime", "/dev/mapper/"+c.EncryptedDevice, c.Mountpoint)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("failed to mount device: %v", err)
	}

	logger.Info("mounted device")

	return nil
}
