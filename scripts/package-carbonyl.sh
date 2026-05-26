#!/usr/bin/env bash
# Bundle the Carbonyl release build into a deterministic tarball under bundle/.
#
# Carbonyl's own scripts/copy-binaries.sh assumes a monolithic build with
# libEGL.so/libGLESv2.so split out as separate files — our build uses
# is_component_build=true + use_static_angle=true so neither assumption holds.
# Instead, walk the actual `ldd` closure from the entry points and copy only
# what's reachable. Result is ~410 .so files + a handful of data blobs.
#
# Determinism: tarball uses fixed uid/gid/mtime so repeated runs over the same
# build inputs produce a byte-identical archive. The sha256 lands next to it
# so the Dockerfile can verify on extract.
#
# Usage: scripts/package-carbonyl.sh [CHROMIUM_OUT_DIR]
#   CHROMIUM_OUT_DIR defaults to /home/nbn/carbonyl/chromium/src/out/Release

set -euo pipefail

SRC="${1:-/home/nbn/carbonyl/chromium/src/out/Release}"
REPO_ROOT=$(cd "$(dirname -- "$0")/.." && pwd)
BUNDLE_DIR="$REPO_ROOT/bundle"
STAGE_DIR="$(mktemp -d -t carbonyl-stage-XXXXXX)"
TARBALL="$BUNDLE_DIR/carbonyl-linux-x86_64.tar.xz"
SHA_FILE="$BUNDLE_DIR/carbonyl-linux-x86_64.sha256"

cleanup() { rm -rf "$STAGE_DIR"; }
trap cleanup EXIT

if [ ! -x "$SRC/headless_shell" ]; then
    echo "error: $SRC/headless_shell not found or not executable" >&2
    exit 2
fi

mkdir -p "$BUNDLE_DIR"

# Entry points to walk for the .so closure. headless_shell is the binary;
# libvk_swiftshader / libvulkan are dlopen-loaded by GPU init at runtime so
# ldd on headless_shell won't see them transitively — list them explicitly.
ENTRY_POINTS=(
    "$SRC/headless_shell"
    "$SRC/libcarbonyl.so"
    "$SRC/libvk_swiftshader.so"
    "$SRC/libvulkan.so.1"
)

echo "==> Computing transitive .so closure from $SRC"
# ldd resolves each binary's full DT_NEEDED chain. We filter to paths under
# $SRC (which means "in the component-build tree") and take just the basename.
# System libs (libc, libnss3, etc.) come from the host at runtime — Dockerfile
# installs them via apt.
SRC_REAL=$(readlink -f "$SRC")
closure=$(for entry in "${ENTRY_POINTS[@]}"; do
    ldd "$entry" 2>/dev/null || true
done | awk '/=>/ {print $3}' | while read -r path; do
    [ -z "$path" ] && continue
    real=$(readlink -f "$path" 2>/dev/null || true)
    case "$real" in "$SRC_REAL"/*) echo "$(basename "$real")" ;; esac
done | sort -u)

echo "==> Closure: $(echo "$closure" | wc -l) .so files"

# Copy .so closure into staging
while read -r so; do
    [ -z "$so" ] && continue
    cp "$SRC/$so" "$STAGE_DIR/"
done <<< "$closure"

# Copy binary (renamed to carbonyl, the user-facing name from copy-binaries.sh)
cp "$SRC/headless_shell" "$STAGE_DIR/carbonyl"
chmod 0755 "$STAGE_DIR/carbonyl"

# Copy data files. Each is required at runtime by some component:
#  - icudtl.dat: Unicode data tables (i18n)
#  - v8_context_snapshot.bin / snapshot_blob.bin: V8 isolate startup snapshots
#  - headless_lib_data.pak / headless_lib_strings.pak / headless_command_resources.pak: UI resources
#  - vk_swiftshader_icd.json: Vulkan ICD descriptor for the bundled swiftshader
DATA_FILES=(
    icudtl.dat
    v8_context_snapshot.bin
    snapshot_blob.bin
    headless_lib_data.pak
    headless_lib_strings.pak
    headless_command_resources.pak
    vk_swiftshader_icd.json
)
for f in "${DATA_FILES[@]}"; do
    if [ -f "$SRC/$f" ]; then
        cp "$SRC/$f" "$STAGE_DIR/"
    else
        echo "warn: missing data file $f" >&2
    fi
done

# Strip every binary + .so. Symbol level was already 0 at link time so this
# only removes section/string table content; ratio is about 70% retention.
echo "==> Stripping $(find "$STAGE_DIR" -name '*.so*' -o -name carbonyl | wc -l) files"
strip -s "$STAGE_DIR/carbonyl"
find "$STAGE_DIR" -maxdepth 1 -name '*.so*' -exec strip -s {} +

# Capture provenance so the bundle README can reference exact source commits.
CHROMIUM_REPO=$(cd "$SRC" && cd ../.. && pwd)
CARBONYL_REPO=$(cd "$CHROMIUM_REPO/.." && pwd)
chromium_sha=$(git -C "$CHROMIUM_REPO" rev-parse HEAD 2>/dev/null || echo "unknown")
carbonyl_sha=$(git -C "$CARBONYL_REPO" rev-parse HEAD 2>/dev/null || echo "unknown")
build_time=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

cat > "$STAGE_DIR/MANIFEST.txt" <<EOF
Carbonyl pre-built bundle for ssh.night.ms
chromium-sha: $chromium_sha
carbonyl-sha: $carbonyl_sha
built-at:     $build_time
target:       x86_64-unknown-linux-gnu
EOF

# Deterministic tar — fixed mtime/owner/group, sorted by name. Without this,
# byte-identical input directories produce different tarballs across runs and
# the sha256 changes for no reason.
echo "==> Creating tarball $TARBALL"
tar --sort=name --owner=0 --group=0 --numeric-owner --mtime='1970-01-01 00:00:00 UTC' \
    -C "$STAGE_DIR" -cJf "$TARBALL" .

sha256sum "$TARBALL" | awk '{print $1}' > "$SHA_FILE"

size_mb=$(du -m "$TARBALL" | awk '{print $1}')
echo "==> Bundle ready"
echo "    $TARBALL ($size_mb MB)"
echo "    $SHA_FILE: $(cat "$SHA_FILE")"
echo "    chromium $chromium_sha"
echo "    carbonyl $carbonyl_sha"
