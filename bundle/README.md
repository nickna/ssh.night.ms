# Carbonyl runtime bundle

Pre-built [Carbonyl](https://github.com/fathyb/carbonyl) (Chromium-in-the-terminal) binaries used by the `R` ("rich mode") hotkey in the browser screen. The bundle ships in this repo via Git LFS so `docker build` doesn't have to compile Chromium (a ~1.5 h, ~100 GB undertaking).

## Files

- `carbonyl-linux-x86_64.tar.xz` — Git-LFS-tracked tarball. Contains the `carbonyl` binary (renamed `headless_shell`), ~410 Chromium component `.so` files, V8 snapshots, ICU data, and the `.pak` resource bundles. Extracted to `/opt/carbonyl/` in the runtime image.
- `carbonyl-linux-x86_64.sha256` — content hash committed normally (not LFS). The Dockerfile verifies the tarball against this on extract.

## Provenance

| | |
|---|---|
| Target | `x86_64-unknown-linux-gnu` |
| Carbonyl fork | https://github.com/nickna/carbonyl (`chromium-upgrade` branch) |
| Carbonyl SHA | `68012ed835f99d831dc67f15f01284a9b2625a83` |
| Chromium SHA | `f161c3350a525032f0b6bc9f7c98bd0a94ccaebe` (~M148) |
| Build args | `is_debug=false is_component_build=true symbol_level=0 blink_symbol_level=0 use_static_angle=true` |
| Bundle compressed | ~103 MB |
| Bundle extracted | ~560 MB |

Component build (not monolithic) because a static-link release crashes in libc's TLS init before main on this Chromium version.

## Rebuilding

```sh
# 1. In the Carbonyl source tree, do a release build (see carbonyl repo CLAUDE.md):
cd /path/to/carbonyl
./scripts/build.sh Release

# 2. Repackage the bundle (overrides the source dir if not at the default):
./scripts/package-carbonyl.sh [/path/to/carbonyl/chromium/src/out/Release]

# 3. The script regenerates both files atomically:
git add bundle/carbonyl-linux-x86_64.tar.xz bundle/carbonyl-linux-x86_64.sha256
```

The script writes a deterministic tarball (sorted entries, fixed mtime/uid/gid) so byte-identical inputs produce byte-identical output — the sha256 only changes when the build actually changed.

## What's in the closure

The tarball is built by walking `ldd`'s transitive output from the four entry points (`headless_shell`, `libcarbonyl.so`, `libvk_swiftshader.so`, `libvulkan.so.1`) against the component build's `out/Release/` dir. Anything not reachable from those entry points isn't shipped.

`copy-binaries.sh` upstream in the Carbonyl repo assumes a monolithic build with separate `libEGL.so`/`libGLESv2.so` and a fixed file list — it doesn't work for our component + static-ANGLE build. `scripts/package-carbonyl.sh` is the replacement.

## Runtime system dependencies

The component .so files have a closure of 36 system libraries. The container image (`debian:bookworm-slim`) installs the packages that supply them:

```
libasound2 libexpat1 libfontconfig1 libnss3 ca-certificates
```

Those four pull in the rest transitively (glib, freetype, harfbuzz, pango, udev, zlib, brotli, NSPR).
