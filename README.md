# hgw

A "secure enclave" I run at home. It runs with a custom-built Linux 6.1 kernel
with the PREEMPT_RT patchset, and is only tested to work on iMX6ULL hardware at
this time.

- Securely manage encryption keys and API tokens without depending on public
  services
- Perform real-time control tasks
  - Reset external devices with GPIO
- Give an unprivileged SSH shell for convenient external access to home network
- Hot-upgradable (signed image + kexec)

## Security properties

- Extracting secrets, or otherwise breaking the integrity of the system, is
  impossible once the device has booted, unless:
  - You managed to dump the DDR RAM. This can usually happen in two ways:
    - Cold-boot attack. Can be prevented by enabling secure boot on the device.
    - The DDR RAM is desoldered from the board while the device is running and
      read out externally.
  - You control the regular private key used for signing the kexec image, _and_
    a record is published to [Sigstore](https://www.sigstore.dev/)
  - You control the "backdoor" OpenPGP private key
- You cannot break timing guarantees of real-time tasks or indefinitely delay
  regular privileged tasks even with SSH access, unless one of the following
  conditions holds:
  - You have physical access to the device
  - You control the regular private key used for signing the kexec image, _and_
    a record is published to [Sigstore](https://www.sigstore.dev/)
  - You control the "backdoor" OpenPGP private key

## Liveness properties

- It is hot-upgradable from internet
- In the event of a crash, it is recoverable from _local_ network
  - It's impossible to compromise security of the device from local network,
    i.e. all the security checks for hot upgrades should still be enforced
- In the event of a loss of power, you have to use a USB cable to re-bootstrap
  the device

## Build and run

1. Clone this repository with submodules.
2. Install Go, Rust, `cargo-zigbuild`, `gcc-arm-linux-gnueabi` and Docker.
3. Install dependencies for building Linux, for example
   `sudo apt-get build-dep linux`.
4. Place your PEM-encoded ECC secp256r1 public key at
   `homegw-libs/keyring/kms.pub`. It should start with
   `-----BEGIN PUBLIC KEY-----`. Sigstore inclusion promise is required for
   binaries signed with this key. It's recommended to put the private key
   somewhere easily accessible yet reasonably secure, e.g. AWS KMS.
5. Place your armored OpenPGP public key at `homegw-libs/keyring/pubkey.asc`. It
   should start with `-----BEGIN PGP PUBLIC KEY BLOCK-----`. Sigstore inclusion
   promise is NOT required for binaries signed with this key, so make sure you
   keep the private key securely, e.g. generated on a Yubikey.
6. Run `make run-dev`.
