scratch-init:
	cd homegw-init && CGO_ENABLED=0 go build -o ../build/scratch/homegw-init

run-init:
	HOMEGW_CONFIG_PATH=build/scratch/config.json build/scratch/homegw-init

run-dev:
	./scripts/build_rootfs.sh
	cd linux && \
		make ARCH=arm CROSS_COMPILE=arm-linux-gnueabi- maglev_imx6_defconfig && \
		make ARCH=arm CROSS_COMPILE=arm-linux-gnueabi- -j4
	make qemu

qemu:
	cat ./linux/arch/arm/boot/zImage ./linux/arch/arm/boot/dts/imx6ul-qemu.dtb > ./build/zImage-imx6ul-qemu
	./scripts/gen_config_ocram_image.sh < ./build/scratch/config.json > build/ocram-qemu.img
	qemu-system-arm -machine mcimx6ul-evk -nographic -m size=512M \
		-serial mon:stdio \
		-device loader,file=./build/ocram-qemu.img,addr=0x901000 \
		-nic user -nic tap,helper=/usr/lib/qemu/qemu-bridge-helper,id=hn0,br=virbr0 \
		-kernel ./build/zImage-imx6ul-qemu || true

seal:
	cat ./linux/arch/arm/boot/zImage ./linux/arch/arm/boot/dts/imx6ul-qemu.dtb > ./build/zImage-imx6ul-qemu.seal
	cat ./build/scratch/config.json | jq --raw-output .kexec_encryption_key | base64 -d > ./build/kexec_encryption_key.bin
	go run ./kexec-sealer/main.go -key ./build/kexec_encryption_key.bin -kernel ./build/zImage-imx6ul-qemu.seal -config ./build/scratch/config.json > ./build/sealed-image.bin

.PHONY: scratch-init run-init run-dev qemu
