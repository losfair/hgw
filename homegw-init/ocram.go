package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"syscall"
)

const OCRAM_START = 0x90_1000
const OCRAM_END = 0x92_0000

func loadConfigFromOcram() ([]byte, bool) {
	ocram := dumpAndEraseOcram()
	expectedConfigHash := ocram[:32]
	config := ocram[32:]

	// Config is zero-terminated, find the zero byte
	var dataLen int
	for i, b := range config {
		if b == 0 {
			dataLen = i
			break
		}
	}

	config = config[:dataLen]
	actualConfigHash := sha256.Sum256(config)
	if !bytes.Equal(expectedConfigHash, actualConfigHash[:]) {
		return nil, false
	}

	return config, true
}

func writeConfigToOcram(config []byte) error {
	if 32+len(config)+1 > OCRAM_END-OCRAM_START {
		return fmt.Errorf("config is too big")
	}

	devMem, err := os.OpenFile("/dev/mem", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("failed to open /dev/mem: %v", err)
	}
	defer devMem.Close()

	ocramMapping, err := syscall.Mmap(int(devMem.Fd()), OCRAM_START, OCRAM_END-OCRAM_START, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return fmt.Errorf("failed to mmap OCRAM: %v", err)
	}
	defer syscall.Munmap(ocramMapping)

	configHash := sha256.Sum256(config)
	copy(ocramMapping[:32], configHash[:])
	copy(ocramMapping[32:32+len(config)], config)
	ocramMapping[32+len(config)] = 0
	return nil
}

func dumpAndEraseOcram() []byte {
	devMem, err := os.OpenFile("/dev/mem", os.O_RDWR, 0)
	if err != nil {
		log.Fatalf("Failed to open /dev/mem: %v", err)
	}
	defer devMem.Close()

	ocramMapping, err := syscall.Mmap(int(devMem.Fd()), OCRAM_START, OCRAM_END-OCRAM_START, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		log.Fatalf("Failed to mmap OCRAM: %v", err)
	}
	defer syscall.Munmap(ocramMapping)

	ocram := make([]byte, OCRAM_END-OCRAM_START)
	copy(ocram, ocramMapping)
	for i := range ocramMapping {
		ocramMapping[i] = 0
	}

	return ocram
}
