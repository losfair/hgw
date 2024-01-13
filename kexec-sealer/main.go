package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"golang.org/x/crypto/chacha20poly1305"
)

var MagicHeader = append([]byte("HGW-KEXEC"), 0)

func main() {
	kernel := flag.String("kernel", "", "kernel image path")
	config := flag.String("config", "", "config path")
	key := flag.String("key", "", "key path")
	flag.Parse()

	keyData, err := os.ReadFile(*key)
	if err != nil {
		log.Fatalf("can't read key: %v", err)
	}

	if len(keyData) != 32 {
		log.Fatalf("key is not 32 bytes long")
	}

	aead, err := chacha20poly1305.NewX(keyData)
	if err != nil {
		log.Fatalf("can't initialize AEAD: %v", err)
	}

	nonce := make([]byte, aead.NonceSize())
	_, err = rand.Read(nonce)
	if err != nil {
		log.Fatalf("can't generate nonce: %v", err)
	}

	kernelData, err := os.ReadFile(*kernel)
	if err != nil {
		log.Fatalf("can't read kernel: %v", err)
	}

	configData, err := os.ReadFile(*config)
	if err != nil {
		log.Fatalf("can't read config: %v", err)
	}

	buf := bytes.NewBuffer(nil)
	binary.Write(buf, binary.LittleEndian, uint32(len(kernelData)))
	binary.Write(buf, binary.LittleEndian, kernelData)
	binary.Write(buf, binary.LittleEndian, uint32(len(configData)))
	binary.Write(buf, binary.LittleEndian, configData)
	data := buf.Bytes()

	dataHash := sha256.Sum256(data)
	signingPayload := fmt.Sprintf("%s  homegw-kexec.v1.bin", hex.EncodeToString(dataHash[:]))
	fmt.Fprintf(os.Stderr, "Please sign the following string with `gpg --clearsign`:\n\n%s\n\n", signingPayload)

	// Read signature from stdin, until EOF
	signature, err := io.ReadAll(os.Stdin)
	if err != nil {
		log.Fatalf("can't read signature: %v", err)
	}

	out := make([]byte, 0, 4+len(signature)+4+len(data)+aead.Overhead())
	out = binary.LittleEndian.AppendUint32(out, uint32(len(signature)))
	out = append(out, signature...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(data)))
	out = append(out, data...)
	out = aead.Seal(out[:0], nonce, out, nil)

	os.Stdout.Write(MagicHeader)
	os.Stdout.Write(nonce)
	os.Stdout.Write(out)
}
