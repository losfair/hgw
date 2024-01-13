package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"time"

	"go.uber.org/zap"
)

type NetifConfig struct {
	Name string `json:"name"`
	Mode string `json:"mode"` // "dhcp", "static"

	Ipv4Address string   `json:"ipv4_address"`
	Ipv4Gateway string   `json:"ipv4_gateway"`
	Nameservers []string `json:"nameservers"`
}

func (c *NetifConfig) Start(logger *zap.Logger) error {
	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("failed to create pipe: %w", err)
	}
	go func() {
		defer pr.Close()

		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			logger.Info("netif log", zap.String("line", scanner.Text()))
		}
	}()

	switch c.Mode {
	case "dhcp":
		go func() {
			for {
				cmd := exec.Command("udhcpc", "-i", c.Name, "-f")
				cmd.Stdout = pw
				cmd.Stderr = pw

				err := cmd.Run()
				logger.Error("udhcpc exited", zap.Error(err))
				time.Sleep(5 * time.Second)
			}
		}()

		logger.Info("started udhcp client")
		return nil
	case "static":
		defer pw.Close()

		cmd := exec.Command("ip", "link", "set", c.Name, "up")
		cmd.Stdout = pw
		cmd.Stderr = pw
		err := cmd.Run()
		if err != nil {
			return fmt.Errorf("failed to bring up interface: %w", err)
		}

		if c.Ipv4Address != "" {
			cmd = exec.Command("ip", "addr", "add", c.Ipv4Address, "dev", c.Name)
			cmd.Stdout = pw
			cmd.Stderr = pw
			err = cmd.Run()
			if err != nil {
				return fmt.Errorf("failed to set ipv4 address: %w", err)
			}
		}

		if c.Ipv4Gateway != "" {
			cmd = exec.Command("ip", "route", "add", "default", "via", c.Ipv4Gateway)
			cmd.Stdout = pw
			cmd.Stderr = pw
			err = cmd.Run()
			if err != nil {
				return fmt.Errorf("failed to set ipv4 gateway: %w", err)
			}
		}

		if len(c.Nameservers) > 0 {
			err := func() error {
				resolvConf, err := os.OpenFile("/etc/resolv.conf", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
				if err != nil {
					return fmt.Errorf("failed to open /etc/resolv.conf: %w", err)
				}
				defer resolvConf.Close()

				for _, nameserver := range c.Nameservers {
					_, err = fmt.Fprintf(resolvConf, "nameserver %s\n", nameserver)
					if err != nil {
						return fmt.Errorf("failed to write to /etc/resolv.conf: %w", err)
					}
				}
				return nil
			}()

			if err != nil {
				return err
			}
		}

		logger.Info("configured static network interface")
		return nil
	default:
		pw.Close()
		return fmt.Errorf("unknown netif mode: %s", c.Mode)
	}
}
