package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"

	kafka_sink "github.com/losfair/hgw/homegw-libs/kafka-sink"
	"go.uber.org/zap"
	"golang.org/x/sys/unix"
)

func loadConfig() (*InitConfig, []byte) {
	var configText []byte

	configPath := os.Getenv("HOMEGW_CONFIG_PATH")
	if configPath == "" {
		text, ok := loadConfigFromOcram()
		if ok {
			configText = text
		} else {
			log.Println("can't load config from ocram, falling back to /dev/ttyGS0")
			configText = loadConfigFromGs0()
		}
	} else {
		text, err := os.ReadFile(configPath)
		if err != nil {
			log.Fatalf("can't read %s: %v", configPath, err)
		}
		configText = text
	}

	var config InitConfig
	err := json.Unmarshal(configText, &config)
	if err != nil {
		log.Fatalf("can't parse config: %v", err)
	}

	return &config, configText
}

func setupLogging(kafkaUrl string) *zap.Logger {
	zap.RegisterSink("kafka", kafka_sink.InitKafkaSink)

	c := zap.NewProductionConfig()
	if kafkaUrl != "" {
		c.OutputPaths = append(c.OutputPaths, kafkaUrl)
	} else {
		log.Println("kafka sink url is empty")
	}
	logger, err := c.Build()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}

	return logger
}

func loadConfigFromGs0() []byte {
	gs0, err := os.OpenFile("/dev/ttyGS0", os.O_RDWR, 0)
	if err != nil {
		log.Fatalf("can't open /dev/ttyGS0: %v", err)
	}
	defer gs0.Close()

	// Turn off echo on gs0
	{
		termios, err := unix.IoctlGetTermios(int(gs0.Fd()), unix.TCGETS)
		if err != nil {
			log.Fatalf("can't get termios: %v", err)
		}

		newState := *termios
		newState.Lflag &^= unix.ECHO
		if err := unix.IoctlSetTermios(int(gs0.Fd()), unix.TCSETS, &newState); err != nil {
			log.Fatalf("can't set termios: %v", err)
		}
	}

	scanner := bufio.NewScanner(gs0)

	for {
		fmt.Fprintf(gs0, "Waiting for config input\n")

		for scanner.Scan() {
			if scanner.Text() == "<BEGIN>" {
				break
			}
		}

		if !scanner.Scan() {
			log.Fatalf("can't read config hash")
		}
		configHashB64 := scanner.Text()

		configDataB64 := ""
		for scanner.Scan() {
			if scanner.Text() == "<END>" {
				break
			}
			configDataB64 += scanner.Text()
		}

		expectedHash, err := base64.StdEncoding.DecodeString(configHashB64)
		if err != nil {
			log.Printf("can't decode config hash: %v", err)
			continue
		}
		if len(expectedHash) != 32 {
			log.Printf("config hash is not 32 bytes")
			continue
		}

		configData, err := base64.StdEncoding.DecodeString(configDataB64)
		if err != nil {
			log.Printf("can't decode config data: %v", err)
			continue
		}
		actualHash := sha256.Sum256(configData)
		if !bytes.Equal(actualHash[:], expectedHash) {
			log.Printf("config hash mismatch")
			continue
		}

		fmt.Fprintf(gs0, "Config received\n")
		return configData
	}
}
