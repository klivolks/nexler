# Building the macOS installer

Automated by `.github/workflows/release.yml` on every `v*` tag push (the `macos-installer` job,
running on GitHub's `macos-latest` runner — `pkgbuild`/`productbuild`/`sips`/`iconutil` all ship
preinstalled there as part of Xcode Command Line Tools).

**Important: this whole directory was written on a Windows machine and has never been built or run
on real macOS.** `pkgbuild`/`productbuild`/`sips`/`iconutil` are macOS-native tools with no Windows
equivalent, so nothing here could be executed while writing it — only `build-pkg.sh` and
`postinstall`'s **shell syntax** were checked (`bash -n`). The first real exercise of this whole
path happens on the `macos-latest` CI runner on the first real tag push, or whenever someone with
an actual Mac runs it by hand. Treat it as a careful, well-researched design that still needs real
verification, not as already-proven.

## Prerequisites (macOS only)

- Xcode Command Line Tools (`xcode-select --install`) — provides `pkgbuild`, `productbuild`,
  `sips`, `iconutil`.
- Go 1.26+, to build the payload binaries.

## Building locally

```
mkdir -p installer/macos/payload
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags "-s -w -X main.cliVersion=0.1.0" -o installer/macos/payload/nexler-amd64 ./cmd/nexler
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "-s -w -X main.cliVersion=0.1.0" -o installer/macos/payload/nexler-arm64 ./cmd/nexler
bash installer/macos/build-pkg.sh 0.1.0
```

Produces `installer/macos/Nexler.pkg`. The script also produces `installer/macos/build/` (staging
root, `.iconset`, generated `distribution.xml`) — safe to delete, regenerated every run.

## Regenerating `nexler-1024.png`

Only needed if `../nexler-icon.svg` (Nexler's own app icon — a dedicated "N" monogram, distinct
from `internal/scaffold/templates/templates/static/logo.svg`, the generic starter logo scaffolded
*into apps built with* nexler) changes. Same tool that generates `installer/windows/nexler.ico`:

```
cd build/gen-icons
go run . ../../installer/nexler-icon.svg ../../installer/macos/nexler-1024.png 1024
```

`build-pkg.sh` resizes this master PNG down to every size an `.icns` needs via `sips`, then packs
with `iconutil` — deliberately never asks `sips` to decode the SVG directly, since `sips`'s own SVG
support is inconsistent across macOS versions and wasn't something the (Windows) machine that wrote
this could verify. Only the already-verified Go rasterizer touches the SVG.

## What to verify by hand once this actually runs on a Mac

- `lipo -info installer/macos/build/staging/bin/nexler` reports both `x86_64` and `arm64`.
- Running `Nexler.pkg` (expect a Gatekeeper block on an unsigned pkg the first time — see
  "Known limitation" below; `xattr -d com.apple.quarantine installer/macos/Nexler.pkg` to proceed
  for testing) shows the Welcome and Read Me panes, installs without error, and:
  - `/usr/local/bin/nexler version` works in a **new** terminal (not one already open) and reports
    the right injected version.
  - `/Applications/Nexler.app` exists, has an icon, and double-clicking it opens Terminal running
    `nexler help`.
  - A `Nexler.app` alias appears on the Desktop.
  - On a machine that's missing Go 1.26+/npm, `postinstall`'s log (`sudo log show
    --predicate 'eventMessage contains "nexler postinstall"' --last 5m` or just watch
    Installer.app's own progress) shows the download-and-silent-install path actually running; on
    a machine that already has both, it should cleanly skip both and say so.
- `pkgutil --forget com.klivolks.nexler` cleans up the package receipt for repeat local testing
  (this does **not** remove installed files — pkg installs have no built-in uninstaller by Apple's
  own convention; not built here, out of scope unless asked separately).

## Known limitation: destination is fixed, not user-chosen

A standard `.pkg` only offers a destination *volume* picker (see `distribution.xml.tmpl`'s
`<domains enable_localSystem="true"/>`), never an arbitrary folder path — achieving a true
folder-picker needs a full code-signed Objective-C/Swift Installer plugin, disproportionate here.
Nexler always installs to `/usr/local/nexler` on macOS. The Windows installer, by contrast, does
offer a real folder picker (native to Inno Setup) — an intentional, documented per-platform
difference, not an oversight.

## Fixed: `postinstall` shipped non-executable

The first real install attempt on a Mac (an Apple Silicon machine) failed outright with
`PKInstallErrorDomain Code=112` and `./postinstall: arch: ... isn't executable` in the install log —
exactly the kind of issue the disclaimer above predicted. `installer/macos/postinstall` was tracked
in git with mode `100644` (no exec bit), since it was authored on a Windows machine, which doesn't
preserve the Unix exec bit the same way; `build-pkg.sh` handed it straight to `pkgbuild --scripts`
without ever `chmod +x`-ing it first, so every built `Nexler.pkg` shipped a script the installer
couldn't run. The "Architecture Translation ... re-executed as Intel" log lines that appeared before
the failure were a side effect of this, not a separate bug — macOS's `arch` launcher falling back to
a Rosetta-translation attempt because it couldn't identify how to run a file it couldn't exec at all.
Fixed by `chmod +x`-ing the tracked file and adding a defensive `chmod +x "$SCRIPT_DIR/postinstall"`
in `build-pkg.sh` right before the `pkgbuild` step, so a future accidental loss of the exec bit can't
silently ship a broken package again.

## Known limitation: unsigned/unnotarized package

No Apple Developer ID exists for this project. Gatekeeper will block `Nexler.pkg` outright
("cannot be opened because it is from an unidentified developer") on first attempt — worse friction
than Windows' SmartScreen, since there's no in-dialog "run anyway" escape hatch for a plain
double-click. Testers need `xattr -d com.apple.quarantine Nexler.pkg` or an explicit System
Settings → Privacy & Security override. Full fix needs signing (`productsign`/`pkgbuild --sign`)
and notarization (`xcrun notarytool` + stapling) — not pursued in this pass.
