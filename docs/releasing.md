# Releasing

A version tag publishes a release. The [release workflow](../.github/workflows/release.yml) does every step; nothing is built on a local machine.

## Publish a release

1. Make sure that `main` has the commit to release and that CI on it passed. The workflow refuses a tag on a commit that is not on `main`.
2. Choose the version by semantic versioning. A tag with a suffix, such as `v0.2.0-rc.1`, makes a prerelease: it gets a GitHub release, but the Homebrew formula keeps the last full release.
3. Before a new minor or major version, push a prerelease first, while the notes are still under `## [Unreleased]`: a prerelease without its own section takes its notes from there. It runs every step, signing and notarization included, and proves that the tap key can push with a dry run. A dry run does not evaluate the tap's branch rules; the tap's `main` blocks only force pushes and deletion, which a release push never does.

   ```sh
   git tag v0.2.0-rc.1
   git push origin v0.2.0-rc.1
   ```

4. After the prerelease passes, in `CHANGELOG.md` rename `## [Unreleased]` to `## [X.Y.Z] - YYYY-MM-DD`, add a new empty `## [Unreleased]` above it, update the links at the end, and merge that change to `main`. The release notes are that section; the workflow refuses a full release without it.
5. Tag the new commit and push the tag:

   ```sh
   git tag v0.2.0
   git push origin v0.2.0
   ```

6. Follow the run with `gh run watch` or `gh run list --workflow=release.yml`.

Releases are immutable: once published, the archives cannot change and the tag cannot move. A release with a mistake needs a new patch version.

## What the workflow does

The build steps are scripts in [`scripts/release/`](../scripts/release/). The [release check](../.github/workflows/release-check.yml) runs the same scripts without secrets on every pull request that touches the release, so a broken build fails in review, not at tag time.

1. Checks that the tag is a version, that its commit is on `main`, and that `CHANGELOG.md` has its notes (`scripts/release/changelog-notes.sh`). Then it runs `make` and `govulncheck` on Linux and on macOS. No release job restores a Go cache that another run saved: `setup-go` runs with `cache: false`, so each run builds from a clean module and build cache.
2. Builds static Linux binaries for amd64 and arm64, each in its own job. `amgd` links SQLite statically against musl, so the binaries need no C library on the target. A step runs them in an empty `busybox` container to prove it.
3. Makes the macOS binaries for arm64 and amd64 in three jobs, so that no job both holds the signing keys and runs code from the build:
   - **Build macOS** builds them with a minimum macOS version (`macos_min` in `scripts/release/build-macos.sh`). It has no secrets.
   - **Sign and notarize macOS** signs them with the Developer ID identity and fixed identifiers (`SIGN_ID_PREFIX`), deletes the signing keychain, notarizes them, and packs them. It never builds and never runs the binaries. Only `codesign` may use the signing key.
   - **Verify macOS** checks the signatures and the notarization, and runs the binaries. It has no secrets.

4. Packs each platform as `agentmailguard_VERSION_OS_ARCH.tar.gz` with `amg`, `amgd`, `LICENSE`, `README.md`, `CHANGELOG.md`, `SECURITY.md`, the `docs` folder, the agent skill in `skills`, and `THIRD_PARTY_LICENSES` (`scripts/release/package.sh`). `scripts/release/licenses.sh` writes the notices with [go-licenses](https://github.com/google/go-licenses): the license of every module compiled in, the Go license, and, for Linux, musl's. It fails if a module has a license that is not Apache-2.0, BSD-2-Clause, BSD-3-Clause, ISC, or MIT.
5. The **Collect archives and check the formula** job, with no secrets, takes the four archives, writes `checksums.txt`, fills in [the formula template](../packaging/homebrew/agentmailguard.rb), checks it with `brew style` and `brew audit --strict` in a temporary tap (`scripts/release/check-formula.sh`), and takes the release notes from `CHANGELOG.md`.
6. The **Publish release** job writes a [build provenance attestation](https://docs.github.com/en/actions/security-for-github-actions/using-artifact-attestations) and creates the GitHub release with the archives, `checksums.txt`, and the notes. It is the only job that can write, and it builds nothing and runs no Homebrew.
7. It pushes the formula to `Formula/agentmailguard.rb` in [ivanrvpereira/homebrew-tap](https://github.com/ivanrvpereira/homebrew-tap) over SSH with a deploy key, then waits until the tap's CI has installed and tested it on macOS (arm64 and Intel) and Linux (amd64 and arm64). A prerelease only checks that the key can push.

Each job seals what it hands on with `scripts/release/digest.sh` and passes the digest to the next job as a job output. The next job stops if a file does not match or an extra file is present, so a file swapped between jobs is never signed or published.

Every binary reports the tag through `amg --version` and `amgd --version`. The workflow sets it with the linker flag `-X github.com/ivanrvpereira/agentmailguard/internal/version.version=TAG`. A build without that flag reports the version that Go records from Git, or `dev`.

## Secrets

The secrets are in the `release` environment (Settings > Environments), which only `v*` tags can use. Set one with `gh secret set NAME --env release`.

| Secret | Use |
|---|---|
| `DEVELOPER_ID_CERT_P12`, `DEVELOPER_ID_CERT_PASSWORD` | The Developer ID Application certificate with its private key, as a base64 `.p12`, and the `.p12` password. |
| `NOTARY_API_KEY_ID`, `NOTARY_API_ISSUER_ID`, `NOTARY_API_KEY_P8` | An App Store Connect **Team** API key for `notarytool`; individual keys cannot notarize. Store the raw `.p8` text, not base64. |
| `TAP_DEPLOY_KEY` | The private half of an SSH deploy key with write access to `ivanrvpereira/homebrew-tap` only. Without it, the release job fails after the GitHub release exists, and the tap keeps the previous formula. |

## Do not change

- **The signing identity and identifiers.** macOS gives a Keychain item to the code that has the team ID and identifier of the program that created it. A new certificate team, or a new `SIGN_ID_PREFIX`, makes every macOS user approve `amgd`'s Keychain access again.
- **`macos_min` below the Go minimum.** When a new Go release needs a newer macOS, `scripts/release/build-macos.sh` fails with "raise macos_min". Raise it, and change the macOS version in [Install and build](install.md#install).

## Troubleshooting

- **Notarization fails with HTTP 403, "A required agreement is missing or has expired".** The Apple Developer account holder must accept the new agreement at developer.apple.com. HTTP 401 means a wrong key ID or issuer ID, or a key that is not a Team key.
- **The notary log reports issues.** The log is in the output of the "Notarize binaries" step.
- **`brew audit` fails.** Change the template in this repository, not the copy in the tap. To check it on your machine, fill it in with `scripts/release/render-formula.sh VERSION CHECKSUMS > /tmp/agentmailguard.rb`, then run `scripts/release/check-formula.sh /tmp/agentmailguard.rb`. The check uses a temporary tap and removes it.
- **The tap's CI fails after the push.** The release on GitHub is already public. Fix the template here, then publish a new patch version.
- **"The tagged commit is not on main".** Tag a commit of `main`. Delete the wrong tag with `git push origin :refs/tags/vX.Y.Z`; the tag ruleset lets only the maintainer do that.
- **"CHANGELOG.md has no notes".** Add the `## [X.Y.Z] - YYYY-MM-DD` section on `main`, then tag the new commit.
- **"Not allowed license".** A new dependency has a license outside the list in `scripts/release/licenses.sh`. Check that the license allows binary distribution before you add it to the list.
