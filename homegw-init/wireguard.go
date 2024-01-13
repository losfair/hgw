package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/losfair/hgw/homegw-libs/memfd"
	"go.uber.org/zap"
)

type WireguardConfig struct {
	Interface  string          `json:"interface"`
	PrivateKey string          `json:"private_key"`
	Addresses  []string        `json:"addresses"`
	Peers      []WireguardPeer `json:"peers"`
}

type WireguardPeer struct {
	PublicKey           string   `json:"public_key"`
	AllowedIPs          []string `json:"allowed_ips"`
	Endpoint            string   `json:"endpoint"`
	PresharedKey        string   `json:"preshared_key"`
	PersistentKeepalive int      `json:"persistent_keepalive"`
}

func (c *WireguardConfig) Apply(logger *zap.Logger) error {
	err := exec.Command("ip", "link", "add", c.Interface, "type", "wireguard").Run()
	if err != nil {
		return fmt.Errorf("failed to create interface: %v", err)
	}

	for _, addr := range c.Addresses {
		err := exec.Command("ip", "addr", "add", addr, "dev", c.Interface).Run()
		if err != nil {
			logger.Error("failed to add address", zap.String("address", addr), zap.Error(err))
		}
	}

	{
		privateKey, err := memfd.NewMemfdReadonlyBuffer("private-key.pem", []byte(c.PrivateKey))
		if err != nil {
			return fmt.Errorf("failed to create memfd for private key: %v", err)
		}

		cmd := exec.Command("wg", "set", c.Interface, "listen-port", "0", "private-key", "/proc/self/fd/3")
		cmd.ExtraFiles = []*os.File{privateKey}
		err = cmd.Run()
		privateKey.Close()

		if err != nil {
			return fmt.Errorf("failed to set private key: %v", err)
		}
	}

	for _, peer := range c.Peers {
		presharedKey, err := memfd.NewMemfdReadonlyBuffer("preshared-key.pem", []byte(peer.PresharedKey))
		if err != nil {
			return fmt.Errorf("failed to create memfd for preshared key: %v", err)
		}

		cmd := exec.Command("wg", "set", c.Interface, "peer", peer.PublicKey, "endpoint", peer.Endpoint)
		if peer.PresharedKey != "" {
			cmd.Args = append(cmd.Args, "preshared-key", "/proc/self/fd/3")
		}
		if peer.PersistentKeepalive != 0 {
			cmd.Args = append(cmd.Args, "persistent-keepalive", fmt.Sprintf("%d", peer.PersistentKeepalive))
		}
		if len(peer.AllowedIPs) != 0 {
			cmd.Args = append(cmd.Args, "allowed-ips", strings.Join(peer.AllowedIPs, ","))
		}
		cmd.ExtraFiles = []*os.File{presharedKey}
		err = cmd.Run()
		presharedKey.Close()

		if err != nil {
			logger.Error("failed to set peer", zap.String("public_key", peer.PublicKey), zap.Error(err))
		}
	}

	err = exec.Command("ip", "link", "set", c.Interface, "up").Run()
	if err != nil {
		return fmt.Errorf("failed to set interface up: %v", err)
	}

	logger.Info("wireguard config applied")
	return nil
}
