#!/bin/bash

set -eo pipefail

input="$(cat -)"

echo "<BEGIN>"
echo -n "$input" | sha256sum | cut -d' ' -f1 | xxd -r -p | base64 -w 0
echo ""
echo -n "$input" | base64 -w 80
echo ""
echo "<END>"
