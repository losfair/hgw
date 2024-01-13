package memfd

import (
	"os"

	"golang.org/x/sys/unix"
)

func NewMemfdReadonlyBuffer(name string, data []byte) (*os.File, error) {
	rawfd, err := unix.MemfdCreate(name, unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}

	mfd := os.NewFile(uintptr(rawfd), name)
	err = writeAndSeal(mfd, data)
	if err != nil {
		mfd.Close()
		return nil, err
	}

	return mfd, nil
}

func writeAndSeal(mfd *os.File, data []byte) error {
	if len(data) != 0 {
		err := unix.Ftruncate(int(mfd.Fd()), int64(len(data)))
		if err != nil {
			return err
		}

		mapping, err := unix.Mmap(int(mfd.Fd()), 0, len(data), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
		if err != nil {
			return err
		}
		copy(mapping, data)
		unix.Munmap(mapping)
	}

	_, err := unix.FcntlInt(mfd.Fd(), unix.F_ADD_SEALS, unix.F_SEAL_GROW|unix.F_SEAL_SHRINK|unix.F_SEAL_WRITE|unix.F_SEAL_SEAL)
	if err != nil {
		return err
	}

	return nil
}
