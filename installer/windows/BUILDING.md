# Building the Windows installer

Automated by `.github/workflows/release.yml` on every `v*` tag push (the `windows-installer` job,
which runs on GitHub's `windows-latest` runner — Inno Setup's `ISCC.exe` ships preinstalled there).
This file is for building/testing it locally.

## Prerequisites

- [Inno Setup 6](https://jrsoftware.org/isinfo.php) (6.3+ — needed for the `x64compatible`/`arm64`
  architecture identifiers this script uses). `ISCC.exe` must be on `PATH`.
- Go 1.26+, to build the payload binaries.

## Regenerating `nexler.ico`

One-time asset, already committed — only needs regenerating if `../nexler-icon.svg` (Nexler's own
app icon — a dedicated "N" monogram, distinct from `internal/scaffold/templates/templates/static/
logo.svg`, which is the generic starter logo scaffolded *into apps built with* nexler, not nexler
itself) changes. No ImageMagick/Inkscape needed; a small throwaway Go tool at `build/gen-icons/`
(its own module, kept out of nexler's own `go.mod`) rasterizes the SVG and packs the ICO directly:

```
cd build/gen-icons
go run . ../../installer/nexler-icon.svg ../../installer/windows/nexler.ico
```

(The macOS `.icns` is *not* generated here — `installer/macos/build-pkg.sh` produces it at
packaging time using Apple's own `sips`/`iconutil`, no separate step needed.)

## Regenerating the wizard images

`wizard-image.png` (192x386 — the tall banner on the Welcome/Finished pages) and
`wizard-small-image.png` (76x80 — the small logo in the corner of every other page) are Inno
Setup's own recommended dimensions for `WizardStyle=modern`. Only need regenerating if
`../wizard-banner.svg`/`../wizard-small.svg` change — each is authored at its own exact target
aspect ratio (not the square app icon reused/stretched) so rasterizing never distorts it:

```
cd build/gen-icons
go run . ../../installer/wizard-banner.svg ../../installer/windows/wizard-image.png 192x386
go run . ../../installer/wizard-small.svg ../../installer/windows/wizard-small-image.png 76x80
```

Inno Setup accepts PNG directly for `WizardImageFile`/`WizardSmallImageFile` (confirmed by
compiling with ISCC — no BMP conversion needed, despite older Inno documentation/examples
sometimes assuming BMP).

## Building and compiling locally

```
mkdir -p installer/windows/payload/amd64 installer/windows/payload/arm64
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-s -w -X main.cliVersion=0.1.0" -o installer/windows/payload/amd64/nexler.exe ./cmd/nexler
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -ldflags "-s -w -X main.cliVersion=0.1.0" -o installer/windows/payload/arm64/nexler.exe ./cmd/nexler
ISCC.exe installer\windows\nexler.iss /DVersion=0.1.0
```

Produces `installer/windows/nexler-setup-0.1.0.exe`. Omitting `/DVersion=` falls back to the
literal `0.0.0-dev` default in `nexler.iss`.

## Testing without a full interactive wizard walkthrough

```
nexler-setup-0.1.0.exe /VERYSILENT /SUPPRESSMSGBOXES /LOG=install-test.log /DIR=C:\SomeTestDir
```

Then check `install-test.log`, confirm `C:\SomeTestDir\nexler.exe` runs and reports the right
version, confirm the Start Menu group and (if the machine already has a desktop) the Desktop
shortcut were created, confirm `HKLM\SYSTEM\CurrentControlSet\Control\Session Manager\Environment`'s
`Path` value gained `C:\SomeTestDir`, and confirm running `C:\SomeTestDir\unins000.exe /VERYSILENT
/SUPPRESSMSGBOXES` cleanly removes all of it again, including that PATH entry. Note: a
freshly-added PATH entry is only visible to *new* processes started after install — a shell you
already had open (including one a test script spawns as a child of an already-running session)
won't see it; open a genuinely new terminal to confirm.

**Requires an elevated (admin) session** — `PrivilegesRequired=admin` in `[Setup]`, since the
script writes to the system PATH and can silently install Go/Node's MSIs.

To actually exercise the "Go/npm missing" download-and-install code path, test on a machine (or
clean VM) that doesn't already have Go 1.26+/npm installed — on a machine that already has both,
those two Tasks simply never appear, which is also correct, expected behavior (see `nexler.iss`'s
`NeedGo`/`NeedNpm`).

**Wizard images specifically**: `/VERYSILENT` never renders the wizard UI at all, so it can't
confirm the banner/small-image actually *look* right in place — only that ISCC accepts the PNGs
(size/format) and the install still succeeds with them present. The screen-capture attempt used to
write this doc ran in a session with no real display handle available (headless), so the actual
rendered wizard has not been visually confirmed by whoever/whatever last edited this file — do one
interactive (non-silent) run and eyeball the Welcome page before calling this done.

## Known limitation: unsigned binary

No code-signing certificate exists for this project yet. First-run SmartScreen "Windows protected
your PC" friction is expected — see the "Code-signing / notarization" note in the project's
CLAUDE.md / release plan. Not solved here; documented so it isn't mistaken for a bug.
