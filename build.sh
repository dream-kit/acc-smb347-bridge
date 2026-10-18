#!/bin/sh
# Cross-compile the ARMv7 helpers and stage them in dist/.
#
# Requirements: Go 1.22 or newer (any host architecture).
#
#   ./build.sh
set -eu

cd "$(dirname "$0")"

GO=${GO:-go}
export CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7

echo "building chgctl and chgbridge for linux/arm (armv7) with $($GO version | awk '{print $3}')"
(cd src/chgctl && $GO build -trimpath -ldflags "-s -w" -o ../../dist/chgctl .)
(cd src/chgbridge && $GO build -trimpath -ldflags "-s -w" -o ../../dist/chgbridge .)

chmod 755 dist/chgctl dist/chgbridge
echo
file dist/chgctl dist/chgbridge 2>/dev/null || :
sha256sum dist/chgctl dist/chgbridge 2>/dev/null || :