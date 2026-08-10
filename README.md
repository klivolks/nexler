# Nexler

Nexler is a scaffolding CLI for the Nexler Go web framework — think `django-admin startproject` or
`rails new`. It generates the handlers/services/store/models project layout that apps built on
Nexler follow, wires new routes into an existing app, and vendors front-end static assets from npm.

Nexler itself is a single, self-contained binary — it doesn't need Go or npm installed to run. Those
two are only needed downstream:

- **Go** is needed to build the *apps you scaffold with Nexler* (`go build`/`go run` on the
  generated output) — not to run `nexler` itself.
- **npm** is only needed by `nexler add`, which vendors front-end packages (Bootstrap, jQuery, etc.)
  into a generated app's `templates/static/vendors/`.

If you installed Nexler via the Windows or macOS installer, both are checked and offered
automatically. If either is missing later, just install it yourself — `nexler` keeps working either
way.

## Quickstart

```
nexler create app myapp
cd myapp
go build ./...
go run main.go
```

Add a route to an existing app:

```
nexler create /purchase/verify -module purchase -submodule verify -methods GET,POST -body json
```

Vendor a front-end package:

```
nexler add bootstrap
```

## Full usage

Run `nexler help` for the complete command reference (`create app`, `create <route>`, `init db`,
`init kpass`, `init kgate`, `add`, `version`), including every flag and what each one does.

## Verifying a release

Every release asset is signed with [cosign](https://github.com/sigstore/cosign) using this repo's
key pair (`cosign.pub`, committed here — the private half never is). After downloading, e.g. the
checksums file and an installer:

```
cosign verify-blob --bundle checksums.txt.bundle --key cosign.pub checksums.txt
cosign verify-blob --bundle nexler-setup-1.2.3.exe.bundle --key cosign.pub nexler-setup-1.2.3.exe
```

Verifying `checksums.txt` and then matching a downloaded archive's own `sha256sum` against its
line in that file covers the raw binaries too, without needing a separate signature per archive.

## License

MIT — see [`LICENSE`](LICENSE). Use it however you like.

## More

Development notes, architecture, and design rationale for contributors live in `CLAUDE.md`.

