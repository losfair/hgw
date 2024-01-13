#!/bin/bash

set -ex

cd "$(dirname $0)/../build"

cat ../linux/arch/arm/boot/zImage ../linux/arch/arm/boot/dts/imx6ull-alientek-emmc.dtb > ./zImage-alientek

# package: u-boot-tools
mkimage -n ../scripts/alientek-imximage.cfg -T imximage -e 0x87800000 -d ./zImage-alientek image-alientek.imx
