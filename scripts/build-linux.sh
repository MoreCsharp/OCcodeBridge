#!/usr/bin/env bash
# Build OCcodeBridge as a CPA native plugin on Linux (dist/occodebridge.so).
#
# A CPA native plugin must be linked as a shared library (-buildmode=c-shared),
# so this needs a C toolchain. It never installs one: gcc (or $CC) has to be on
# PATH, which is the case on Debian/Ubuntu after "apt-get install build-essential".
#
#   scripts/build-linux.sh
#
# The build parameters match .github/workflows/release.yml and Dockerfile.build.
# Prints the artifact path, its size in bytes and its sha256.
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
output="$repo_root/dist/occodebridge.so"
compiler="${CC:-gcc}"

if ! command -v go >/dev/null 2>&1; then
	echo "Go toolchain not found on PATH. Install Go 1.26+ and re-run scripts/build-linux.sh." >&2
	exit 1
fi
if ! command -v "$compiler" >/dev/null 2>&1; then
	echo "No C compiler found (CC=$compiler)." >&2
	echo 'dist/occodebridge.so needs "go build -buildmode=c-shared"; install gcc (Debian/Ubuntu: apt-get install build-essential) or point $CC at one. OCcodeBridge does not install compilers.' >&2
	exit 1
fi

# Reads $length bytes starting at $offset of $output as lowercase hex.
read_hex() {
	od -An -v -tx1 -N "$1" -j "$2" "$output" | tr -d ' \n'
}

# The produced file must be a little-endian ELF64 x86-64 shared object, not a
# stale or foreign binary.
assert_elf_x86_64() {
	local magic class endianness machine
	magic=$(read_hex 4 0)
	class=$(read_hex 1 4)
	endianness=$(read_hex 1 5)
	machine=$(read_hex 2 18)
	if [[ $magic != 7f454c46 ]]; then
		echo "$output is not an ELF file (e_ident magic 0x$magic)." >&2
		return 1
	fi
	if [[ $class != 02 ]]; then
		echo "$output is not ELF64 (EI_CLASS 0x$class)." >&2
		return 1
	fi
	if [[ $endianness != 01 ]]; then
		echo "$output is not little-endian (EI_DATA 0x$endianness)." >&2
		return 1
	fi
	if [[ $machine != 3e00 ]]; then
		echo "$output targets ELF machine 0x${machine:2:2}${machine:0:2}, not x86-64 (0x003e)." >&2
		return 1
	fi
}

compiler_version=$("$compiler" --version 2>&1 || true)
compiler_version=${compiler_version%%$'\n'*}

mkdir -p "$repo_root/dist"
echo "Building $output"
echo "CC=$compiler"
echo "compiler=$compiler_version"

cd -- "$repo_root"
CGO_ENABLED=1 go build -trimpath -buildvcs=false -buildmode=c-shared -ldflags="-s -w" -o "$output" ./cmd/occodebridge

if [[ ! -f $output ]]; then
	echo "Build reported success but $output is missing." >&2
	exit 1
fi
assert_elf_x86_64

size=$(wc -c <"$output" | tr -d '[:space:]')
sha256=$(sha256sum "$output" | cut -d' ' -f1)
echo "Built $output ($size bytes) sha256=$sha256"
