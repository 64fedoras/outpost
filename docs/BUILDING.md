# Building and verifying Outpost

Moved out of the README; the README links here.

## Verifying downloads and unsigned binaries

Outpost's release binaries are **not code-signed**. Nothing here claims otherwise, and signing is
still an open decision. What that means in practice:

- **Windows** shows a SmartScreen warning ("Windows protected your PC") for an unsigned exe that was
  downloaded from the internet. Choose **More info → Run anyway**, or right-click the file →
  Properties → **Unblock**. Defender may also flag it (see the Defender note under Install in the [README](../README.md)).
- **macOS** Gatekeeper blocks an unsigned binary that came from a browser. Clear the quarantine flag
  with `xattr -d com.apple.quarantine outpost-darwin-arm64` or right-click → Open.
- **Anyone can rebuild Outpost**, so a binary is only as trustworthy as where you got it. Download from
  this repository's GitHub Releases page and check it against the `SHA256SUMS` file published with
  the same release:

```bash
grep outpost-linux-amd64 SHA256SUMS | sha256sum -c     # Linux ("OK" means it matches)
grep outpost-darwin-arm64 SHA256SUMS | shasum -a 256 -c  # macOS
```
```powershell
(Get-FileHash .\outpost-windows-amd64.exe -Algorithm SHA256).Hash   # compare with the line in SHA256SUMS
```

A matching hash proves the file is the one published with the release; it does not prove who
published it (`SHA256SUMS` is not signed either). If you would rather not trust a download at all,
**build from source** (below) and compare or just use your own build.

## Build from source

You need **Go 1.24.7 or newer** (the `go` line in `go.mod`) and nothing else. There is no cgo, and every
dependency is committed in `vendor/`, so the build needs no network after Go itself is installed (the
scripts turn off module and toolchain downloads on purpose). Get the source with `git clone` or from the
release zip.

**Linux and macOS** (plain `sh`; works with macOS's bash 3.2 and BSD tools):

```bash
sh build.sh current      # just this machine's platform
sh build.sh all          # all five platforms (the default with no argument)
sh build.sh linux-arm64  # one platform: linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64
sh build.sh all ~/out    # optional second argument: where to put the output
```

**Windows** (PowerShell 5.1 or newer; no `sh`, zip tool or signing tool needed):

```powershell
.\build.ps1                      # this machine (windows-amd64)
.\build.ps1 all -Zip             # all five platforms, plus outpost-<version>.zip
.\build.ps1 linux-arm64 -OutDir C:\out
powershell -ExecutionPolicy Bypass -File .\build.ps1    # if scripts are blocked; for a downloaded copy also run Unblock-File .\build.ps1
```

Both scripts check the Go version first and stop with a clear message if Go is missing or too old,
build with `CGO_ENABLED=0 -trimpath`, and print the SHA-256 of every binary.

**Where the output goes:** `../dist/outpost-<version>/bin/` (next to your checkout) unless you name a
directory. Files are named `outpost-<os>-<arch>` (`.exe` on Windows), the same names the Updates tab
expects. To build without the scripts: `CGO_ENABLED=0 go build -trimpath -o outpost .`

**Versions.** The binary's version is the base `const Version` in `proto.go`. For releases, CI stamps the
next patch (or, after patch 19, minor) version over it at build time and commits nothing, so a plain local build reports the base
(`0.8.0`), not the release number. To stamp your own number, set `OUTPOST_VERSION` (or `-Version` in
PowerShell); `proto.go` is put back afterwards, so your checkout stays clean:

```bash
OUTPOST_VERSION=0.8.20 sh build.sh current
```
```powershell
.\build.ps1 -Version 0.8.20
```

**Check what you built:** run `outpost version` (for example `./outpost-linux-amd64 version` prints
`Outpost 0.8.20 linux/amd64`) and compare the hash the script printed.

**Official release versus your build.** Both are unsigned. The builds are reproducible: with the same
Go version, the same source and the same stamped version, two builds on the same machine gave
byte-identical binaries for all five platforms (`-trimpath` removes paths and `-buildid=` removes
Go's random build id). That was checked on Linux with Go 1.24.7; it was not checked across different
operating systems or Go versions, and a different Go version will give different bytes. The Windows exe
also contains version info made by `llvm-rc`/`llvm-cvtres`. The scripts regenerate it when those tools
are installed; without them the committed resource file is used, so a Windows exe built without them
will not match the official hash even from identical source. So: Linux and macOS binaries can be
matched against the release hashes if you use the same Go version and version number; Windows
binaries usually will not.

**Installing a self-built binary.** Updating through the **Updates** tab works the same as for a release:
drop your `outpost-<os>-<arch>` files on the page. The version must be **higher** than the one running,
so build with an `OUTPOST_VERSION` above it (or bump the base for a deliberate release); an equal or lower
version is ignored. To install fresh on a machine, copy the binary there and use the `install` command
as in the Install section.

Signing is optional and off by default: `build.sh` signs the Windows exe only if `OUTPOST_SIGN_PFX`
(needs `osslsigncode`) or `OUTPOST_SIGN_CMD` is set, and `build.ps1` only if `OUTPOST_SIGN_CMD` is set
(`{exe}` stands for the file). Without them the exe is left unsigned.

## Updating from a fork or a private repository

If you build Outpost yourself from a fork, point the controller at it under **Settings → Updates from
GitHub → Repository to trust for updates**. Your fork's workflow must publish a release with the five
binaries and a `SHA256SUMS` file, as this repository's does. For a private fork, create a fine-grained
personal access token limited to that repository with read-only *Contents* access and paste it into the
same section; the README ("Updating") has the steps.
