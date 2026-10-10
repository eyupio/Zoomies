#!/bin/sh
#
# The toolchain a build typically reaches for, per package family.
#
# What is here is what a workflow assumes without ever saying so. The compilers
# and headers are the ones native extensions link against -- node-gyp, a Python
# wheel with no arm64 build, a Ruby gem -- and every one of them fails at `make`
# with a message about the missing library rather than about this image.
#
# python3 and node are here for the steps that use them without a setup-python
# or setup-node first. The setup actions still install their own versions and
# take precedence on PATH, so the versions here are a floor, not a choice about
# which version a workflow gets.
#
# Deliberately absent: a JDK and the other language runtimes with a good setup-*
# action, which install what the workflow asked for rather than what this image
# guessed, and would be hundreds of megabytes of wrong version.
#
# The two families' lists are not translations of each other, because the
# distributions do not split their packages the same way. They are each the
# shortest list that makes the checks at the end pass. lsb_release is in the apt
# list and not the dnf one for that reason: Fedora retired redhat-lsb-core, and
# /etc/os-release -- which every image here has -- is what replaced it.
#
# A fleet's own extra packages are not installed here but in
# deploy/runner-extra.sh, in a layer of its own, so that adding one does not
# reinstall all of this.
#
#   usage: runner-toolchain.sh <apt|dnf>
#
set -eu

family="${1:?usage: runner-toolchain.sh <apt|dnf>}"

case "${family}" in
  apt)
    "$(dirname "$0")/runner-apt.sh" \
      build-essential pkg-config cmake autoconf automake libtool \
      patch file dpkg-dev gettext \
      python3 python3-pip python3-venv python3-dev python3-setuptools \
      nodejs npm \
      libssl-dev zlib1g-dev libffi-dev libyaml-dev libxml2-dev libxslt1-dev \
      libcurl4-openssl-dev libsqlite3-dev libreadline-dev libbz2-dev \
      liblzma-dev libncurses-dev uuid-dev \
      git-lfs wget gnupg bzip2 zstd lsb-release '?software-properties-common' \
      netcat-openbsd dnsutils iputils-ping net-tools
    ;;
  dnf)
    # The RHEL rebuilds keep a good half of the -devel packages a build needs
    # in CodeReady Builder, which ships disabled: libyaml-devel alone is the
    # difference between a Ruby gem or a PyYAML wheel building and not. Fedora
    # carries them in its main repository and has no such thing, so this is
    # attempted and allowed to fail rather than branched on a distribution list
    # that would need editing for every new one.
    if ! grep -q '^ID=fedora' /etc/os-release 2>/dev/null; then
      "$(dirname "$0")/runner-dnf.sh" 'dnf-command(config-manager)'
      dnf config-manager --set-enabled crb 2>/dev/null \
        || dnf config-manager --set-enabled powertools 2>/dev/null \
        || echo "runner-toolchain.sh: no CRB or PowerTools repository to enable; continuing" >&2
    fi
    # The pinned base can carry glibc from a different repository revision than
    # the devel packages, and glibc-devel requires an exact match.
    "$(dirname "$0")/runner-dnf.sh" upgrade
    # Keep compiler packages in the retry helper: Rocky group installs can fail
    # while repository versions are briefly out of sync.
    "$(dirname "$0")/runner-dnf.sh" --allowerasing --setopt=install_weak_deps=False \
      gcc gcc-c++ make \
      pkgconf-pkg-config cmake autoconf automake libtool patch file gettext \
      python3 python3-pip python3-devel python3-setuptools \
      nodejs npm \
      openssl-devel zlib-devel libffi-devel libyaml-devel libxml2-devel libxslt-devel \
      libcurl-devel sqlite-devel readline-devel bzip2-devel \
      xz-devel ncurses-devel libuuid-devel \
      git-lfs wget gnupg2 bzip2 zstd \
      nmap-ncat bind-utils iputils net-tools
    dnf clean all
    rm -rf /var/cache/dnf
    ;;
  *)
    echo "runner-toolchain.sh: unknown package family '${family}'; expected apt or dnf" >&2
    exit 64
    ;;
esac

git lfs install --system
