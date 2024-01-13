#!/bin/bash

# Spawn many processes that each run `a=$(dd if=/dev/zero bs=1K count=512 | base64)`.

set -e

function spawn() {
  # var name suffixed with i
  local name="a$1"
  # var value is 512k of random data
  local value=$(dd if=/dev/zero bs=1K count=512 | base64)
  # export var
  export $name=$value
}

for i in $(seq 1 250); do
    spawn $i &
done

wait
echo "Done"
sleep infinity
