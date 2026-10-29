#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
#
# ci-can-modules.sh: make the CAN modules loadable on a GitHub-hosted Ubuntu
# runner: can, can-raw and vcan (required) and can-isotp (kernel ISO-TP), and vxcan when available.
#
# They are in the kernel's linux-modules-extra package, which runners do not
# install. Some runner kernels (linux-azure on amd64) are built without
# CONFIG_CAN_ISOTP: then net/can/isotp.c of the same kernel version is built
# as an out-of-tree module against the kernel headers. When that fails too,
# the kernel ISO-TP scenarios are skipped by the self-test (the canutils
# adapter ends its ISO-TP operations with exit 4), never reported as passed.
set -euo pipefail

kernel=$(uname -r)
if ! sudo modprobe -a can can-raw vcan 2>/dev/null; then
    echo "Installing linux-modules-extra-${kernel}"
    sudo apt-get update -q
    sudo apt-get install -y -q "linux-modules-extra-${kernel}"
    sudo modprobe -a can can-raw vcan
fi
echo "can, can-raw, vcan loaded on ${kernel}"
sudo modprobe vxcan 2>/dev/null || echo "::warning::vxcan is not available on ${kernel}"
sudo modprobe slcan 2>/dev/null || echo "::warning::slcan is not available on ${kernel}: the kernel slcan test will be skipped"

if sudo modprobe can-isotp 2>/dev/null; then
    echo "can-isotp loaded"
    exit 0
fi

echo "::notice::${kernel} has no can-isotp module; building net/can/isotp.c of Linux ${kernel%%-*} out of tree"
build() {
    local version tag dir
    version=${kernel%%-*}                     # e.g. 6.17.0
    tag="v${version%.0}"                      # v6.17 (a release), v6.17.3 otherwise
    dir=$(mktemp -d)
    sudo apt-get install -y -q "linux-headers-${kernel}" >/dev/null
    curl -fsSL "https://raw.githubusercontent.com/torvalds/linux/${tag}/net/can/isotp.c" -o "${dir}/isotp.c" \
        || curl -fsSL "https://git.kernel.org/pub/scm/linux/kernel/git/stable/linux.git/plain/net/can/isotp.c?h=${tag}" -o "${dir}/isotp.c"
    printf 'obj-m += can-isotp.o\ncan-isotp-y := isotp.o\n' > "${dir}/Kbuild"
    make -s -C "/lib/modules/${kernel}/build" M="${dir}" modules
    sudo insmod "${dir}/can-isotp.ko"
}
if build; then
    echo "can-isotp built from Linux source and loaded"
else
    echo "::warning::could not provide can-isotp on ${kernel}: the kernel ISO-TP tests will be skipped"
fi
