#!/bin/sh
#
# ShellCheck, from its own release rather than a distribution package.
#
# GitHub's hosted runners carry shellcheck, so a workflow that lints its shell
# scripts moves to this fleet and fails with "shellcheck: not found". Ubuntu
# and Debian package it, but the RHEL family only has it in EPEL, and pulling
# a whole third-party repository into every dnf image for one binary is a
# worse trade than one pinned, statically linked download checked against its
# digest -- the same way the runner archive itself is fetched.
#
#   usage: runner-shellcheck.sh <amd64|arm64>
#
set -eu

version=v0.10.0
arch="${1:?usage: runner-shellcheck.sh <amd64|arm64>}"

case "${arch}" in
  amd64)
    file=x86_64
    sha256=6c881ab0698e4e6ea235245f22832860544f17ba386442fe7e9d629f8cbedf87
    ;;
  arm64)
    file=aarch64
    sha256=324a7e89de8fa2aed0d0c28f3dab59cf84c6d74264022c00c22af665ed1a09bb
    ;;
  *)
    echo "runner-shellcheck.sh: unsupported architecture '${arch}'; expected amd64 or arm64" >&2
    exit 64
    ;;
esac

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
curl -fsSL --retry 3 -o "$work/shellcheck.tar.xz" \
  "https://github.com/koalaman/shellcheck/releases/download/${version}/shellcheck-${version}.linux.${file}.tar.xz"
echo "${sha256}  $work/shellcheck.tar.xz" | sha256sum -c -
tar -xJf "$work/shellcheck.tar.xz" -C "$work"
install -m 0755 "$work/shellcheck-${version}/shellcheck" /usr/local/bin/shellcheck
