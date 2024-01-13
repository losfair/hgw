#!/bin/bash

set -ex

cd "$(dirname $0)/.."
go mod tidy
cd homegw-init
GOOS=linux GOARCH=arm CGO_ENABLED=0 go build -o ../rootfs/homegw-init -ldflags="-X main.GitCommit=$(git rev-parse HEAD) -X main.GithubRunId=$GITHUB_RUN_ID"
cd ../homegw-rt
cargo zigbuild --release --target armv7-unknown-linux-gnueabihf.2.17
cp ./target/armv7-unknown-linux-gnueabihf/release/homegw-rt ../rootfs/
cd ..

docker buildx build --platform linux/armhf -t losfair/hgw-rootfs rootfs
container_id=$(docker create losfair/hgw-rootfs 2> /dev/null)

rm -rf build/rootfs && mkdir build/rootfs
cd build/rootfs
docker export "$container_id" | fakeroot tar x
find . | fakeroot cpio -o -H newc > ../rootfs.cpio
cd ../..

docker rm -f "$container_id"
