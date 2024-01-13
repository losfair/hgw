package keyring

import (
	"bytes"
	_ "embed"

	"github.com/ProtonMail/go-crypto/openpgp"
)

//go:embed pubkey.asc
var pubkey []byte

//go:embed rekor.pub
var RekorPub []byte

// KMS key id: fa13a37e-84fb-48d0-a507-34ad383fdee6
//
//go:embed kms.pub
var KmsPub []byte

var Keyring openpgp.EntityList

func init() {
	Keyring, _ = openpgp.ReadArmoredKeyRing(bytes.NewReader(pubkey))
	if Keyring == nil {
		panic("can't read pubkey")
	}
}
