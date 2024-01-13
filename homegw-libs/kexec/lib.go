package kexec

import (
	"bytes"
	"context"
	"crypto"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/losfair/hgw/homegw-libs/keyring"
	"github.com/sigstore/cosign/v2/pkg/cosign"
	"github.com/sigstore/cosign/v2/pkg/oci/static"
	"github.com/sigstore/cosign/v2/pkg/signature"
	"github.com/sigstore/sigstore/pkg/tuf"
	"go.uber.org/zap"
	"golang.org/x/crypto/chacha20poly1305"
)

var sha256sumRegex = regexp.MustCompile(`^([0-9a-f]{64})  (.+)$`)

type UnsealedPackage struct {
	Kernel []byte
	Config []byte
}

type minimalConfig struct {
	Version int64 `json:"version"`
}

var ErrInvalidBlob = errors.New("invalid blob")
var ErrInvalidExtSig = errors.New("invalid external signature")

func Unseal(logger *zap.Logger, currentVersion int64, encryptionKey [32]byte, blob []byte, extSig string) (*UnsealedPackage, error) {
	data, err := decryptAndVerify(logger, encryptionKey, blob, extSig)
	if err != nil {
		return nil, err
	}

	if len(data) < 4 {
		return nil, ErrInvalidBlob
	}
	kernelImageLen := binary.LittleEndian.Uint32(data[:4])
	data = data[4:]

	if len(data) < int(kernelImageLen) {
		return nil, ErrInvalidBlob
	}
	kernelImage := data[:kernelImageLen]
	data = data[kernelImageLen:]

	if len(data) < 4 {
		return nil, ErrInvalidBlob
	}
	configImageLen := binary.LittleEndian.Uint32(data[:4])
	data = data[4:]

	if len(data) != int(configImageLen) {
		return nil, ErrInvalidBlob
	}
	configImage := data

	var minConfig minimalConfig
	err = json.Unmarshal(configImage, &minConfig)
	if err != nil {
		return nil, ErrInvalidBlob
	}

	if minConfig.Version < currentVersion {
		return nil, fmt.Errorf("cannot rollback from %d to %d", currentVersion, minConfig.Version)
	}

	return &UnsealedPackage{
		Kernel: kernelImage,
		Config: configImage,
	}, nil
}

func decryptAndVerify(logger *zap.Logger, encryptionKey [32]byte, blob []byte, extSig string) ([]byte, error) {
	useExtSig := extSig != ""

	if useExtSig {
		pubkeys := cosign.NewTrustedTransparencyLogPubKeys()
		pubkeys.AddTransparencyLogPubKey(keyring.RekorPub, tuf.Active)

		sigVerifier, err := signature.LoadPublicKeyRaw(keyring.KmsPub, crypto.SHA256)
		if err != nil {
			return nil, ErrInvalidExtSig
		}

		co := &cosign.CheckOpts{
			IgnoreSCT:    true,
			Offline:      true,
			RekorPubKeys: &pubkeys,
			SigVerifier:  sigVerifier,
		}

		var payload cosign.LocalSignedPayload
		if err := json.Unmarshal([]byte(extSig), &payload); err != nil {
			return nil, ErrInvalidExtSig
		}

		sig, err := static.NewSignature(blob, payload.Base64Signature, static.WithBundle(payload.Bundle))
		if err != nil {
			return nil, ErrInvalidExtSig
		}

		bundleVerified, err := cosign.VerifyBlobSignature(context.Background(), sig, co)
		if err != nil {
			return nil, err
		}

		if !bundleVerified {
			return nil, fmt.Errorf("bundle not verified")
		}

		bundle, err := sig.Bundle()
		if err != nil {
			return nil, ErrInvalidExtSig
		}

		selfStat, err := os.Stat("/homegw-init")
		if err != nil {
			return nil, fmt.Errorf("failed to stat /homegw-init: %v", err)
		}

		selfModTime := selfStat.ModTime()
		bundleTime := time.Unix(bundle.Payload.IntegratedTime, 0)

		// Reject if bundleTime < selfModTime
		if bundleTime.Before(selfModTime) {
			return nil, fmt.Errorf("bundle time is before self mod time")
		}

		logger.Info("kexec package verified with external signature", zap.Time("self_mod_time", selfModTime), zap.Time("bundle_time", bundleTime))
	}

	aead, err := chacha20poly1305.NewX(encryptionKey[:])
	if err != nil {
		return nil, err
	}

	magicHeader := append([]byte("HGW-KEXEC"), 0)
	if len(blob) < len(magicHeader) {
		return nil, ErrInvalidBlob
	}
	if !bytes.Equal(blob[:len(magicHeader)], magicHeader) {
		return nil, ErrInvalidBlob
	}
	blob = blob[len(magicHeader):]

	if len(blob) < aead.NonceSize() {
		return nil, ErrInvalidBlob
	}

	nonce := blob[:aead.NonceSize()]
	blob = blob[aead.NonceSize():]

	blob, err = aead.Open(blob[:0], nonce, blob, nil)
	if err != nil {
		return nil, err
	}

	// Blob structure:
	// <4-byte LE sig len> + sig + <4-byte LE data len> + data
	if len(blob) < 4 {
		return nil, ErrInvalidBlob
	}
	sigLen := binary.LittleEndian.Uint32(blob[:4])
	blob = blob[4:]

	if len(blob) < int(sigLen) {
		return nil, ErrInvalidBlob
	}
	sig := blob[:sigLen]
	blob = blob[sigLen:]

	if len(blob) < 4 {
		return nil, ErrInvalidBlob
	}
	dataLen := binary.LittleEndian.Uint32(blob[:4])
	blob = blob[4:]

	if len(blob) != int(dataLen) {
		return nil, ErrInvalidBlob
	}
	data := blob

	if !useExtSig {
		sigBlock, _ := clearsign.Decode(sig)
		if sigBlock == nil {
			return nil, fmt.Errorf("failed to decode signature")
		}

		signer, err := sigBlock.VerifySignature(keyring.Keyring, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to verify signature: %v", err)
		}

		submatch := sha256sumRegex.FindStringSubmatch(strings.Split(string(sigBlock.Plaintext), "\n")[0])
		if submatch == nil {
			return nil, fmt.Errorf("failed to parse hash file")
		}

		sha256sum, err := hex.DecodeString(submatch[1])
		if err != nil {
			return nil, fmt.Errorf("failed to parse hash: %v", err)
		}

		filename := submatch[2]
		if filename != "homegw-kexec.v1.bin" {
			return nil, fmt.Errorf("unsupported filename: %s", filename)
		}

		actualHash := sha256.Sum256(data)
		if !bytes.Equal(sha256sum, actualHash[:]) {
			return nil, fmt.Errorf("hash mismatch")
		}
		logger.Info("kexec package verified with pgp signature", zap.Any("signer", signer))
	}

	return data, nil
}
