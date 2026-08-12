#!/usr/bin/env bash
# Cross-compiles the Windows executable from macOS (or Linux) and assembles a
# drop-in folder for the director's machine.
#
# Go cross-compiles without a toolchain for the target, which is what makes
# building a Windows app from a Mac practical. The result is a single static
# .exe with no runtime for her to install.
set -euo pipefail

cd "$(dirname "$0")"

VERSION="${VERSION:-$(date +%Y.%m.%d)}"
OUT="dist/windows"

echo "==> Running tests"
go test ./...

echo "==> Building digest.exe (windows/amd64, version $VERSION)"
mkdir -p "$OUT"

# -s -w strips the symbol table and DWARF data: a smaller binary, and nothing
# is being debugged on the target machine anyway.
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION" \
    -o "$OUT/digest.exe" \
    ./cmd/digest

echo "==> Staging installer"
cp install/Install-Digest.ps1 "$OUT/"
cp install/Uninstall-Digest.ps1 "$OUT/"
cp install/Connect-Claude.ps1 "$OUT/"
cp install/Disconnect-Claude.ps1 "$OUT/"
cp INSTALL-README.txt "$OUT/" 2>/dev/null || true

SIZE=$(du -h "$OUT/digest.exe" | cut -f1)
echo
echo "Built $OUT/digest.exe ($SIZE)"
echo
echo "Send the whole '$OUT' folder to the Windows machine, then run:"
echo "  powershell -ExecutionPolicy Bypass -File .\\Install-Digest.ps1"
echo
echo "Note: the .exe is unsigned, so SmartScreen will warn on first run."
echo "See README section 'Getting past SmartScreen'."
