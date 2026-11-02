# Install and build

Install the released binaries, or build `amg` and `amgd` from source.

## Install

With Homebrew on macOS or Linux:

```sh
brew install ivanrvpereira/tap/agentmailguard
```

Without Homebrew, download the archive for your system from [Releases](https://github.com/ivanrvpereira/agentmailguard/releases), check it against `checksums.txt`, unpack it, and put `amg` and `amgd` on your PATH.
The macOS binaries run on macOS 13 or later; they are signed with a Developer ID and notarized by Apple. Run them from Terminal: the first run of a downloaded copy needs a network connection, so that macOS can confirm the notarization.
The Linux binaries are static and run on any distribution. To check that an archive was built by this repository's release workflow, run `gh attestation verify ARCHIVE --repo ivanrvpereira/agentmailguard`. Releases are immutable: to check that an archive is the one that the release published, run `gh release verify-asset TAG ARCHIVE --repo ivanrvpereira/agentmailguard`.
Each archive has the license notices of the code compiled into the binaries in `THIRD_PARTY_LICENSES`.
Run `amg --version` and `amgd --version` to see the installed version.

To build from source, see [Build and checks](#build-and-checks).

## Build and checks

1. Install a currently supported, patched Go release (minimum Go 1.26) and a C compiler with CGO support: the Xcode Command Line Tools on macOS, or your distribution's compiler toolchain on Linux.
2. Run `make` from this repository.
3. Run `./bin/amgd --help` and `./bin/amg --help`.

`make` checks formatting, runs `go vet` and race tests, builds the packages, and creates `bin/amg` and `bin/amgd`.
`amgd` needs CGO for SQLite and the Keychain. The Keychain backend works only on macOS. Windows is not supported.
`amg` is pure Go and builds with `CGO_ENABLED=0`, so you can build it for the machine where the agent runs and copy it there.
There is no plaintext credential fallback.
The build does not install anything into your PATH. The examples in these docs use `amg` and `amgd` as if they are on your PATH.
A local build is signed ad hoc, not with the release identity. On macOS, the Keychain can ask again for access when you switch between a local build and a release build.
To publish a release, see [Releasing](releasing.md).
