#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
source_dir="$repo_root/native/tun2proxy"
revision="e271de19683937f23d3f8f0eb4df0a61fc4a6e50"

if [[ ! -d "$source_dir/.git" ]]; then
  git clone https://github.com/tun2proxy/tun2proxy.git "$source_dir"
fi
git -C "$source_dir" fetch --tags origin
git -C "$source_dir" checkout --detach "$revision"
# native/tun2proxy is an ignored, reproducible vendor checkout. Restore only
# that pinned checkout so this script can be run repeatedly.
git -C "$source_dir" restore --source="$revision" --staged --worktree -- .
git -C "$source_dir" apply --check "$repo_root/native/patches/android-no-process-exit.patch"
git -C "$source_dir" apply "$repo_root/native/patches/android-no-process-exit.patch"

export ANDROID_NDK_HOME="${ANDROID_NDK_HOME:-${ANDROID_HOME:-$HOME/Android/Sdk}/ndk/29.0.14206865}"
cd "$source_dir"
cargo ndk -t arm64-v8a -t armeabi-v7a -t x86_64 -o "$repo_root/app/src/main/jniLibs" build --release --lib

echo "Built pinned tun2proxy $revision into app/src/main/jniLibs"
