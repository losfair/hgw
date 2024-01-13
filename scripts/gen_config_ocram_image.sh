#!/bin/bash

set -eo pipefail

input="$(cat -)"

echo -n "$input" | sha256sum | cut -d' ' -f1 | xxd -r -p 
echo -n "$input"
echo -ne '\x00'
