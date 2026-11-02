# Releasing

A version tag publishes a release. The [release workflow](../.github/workflows/release.yml) does every step; nothing is built on a local machine.

## Publish a release

1. Make sure that `main` has the commit to release and that CI on it passed.
2. Choose the version by semantic versioning. A tag with a suffix, such as `v0.2.0-rc.1`, makes a prerelease: it gets a GitHub release, but the Homebrew formula keeps the last full release.
3. Tag the commit and push the tag:

   ```sh
   git tag v0.1.0
   git push origin v0.1.0
   ```

4. Follow the run with `gh run watch` or `gh run list --workflow=release.yml`.

## What the workflow does

The build steps are scripts in [`scripts/release/`](../scripts/release/). The [release check](../.github/workflows/release-check.yml) runs the same scripts without secrets on every pull request that touches the release, so a broken build fails in review, not at tag time.

1. Checks that the tag is a version, then runs `make` on Linux and on macOS.
2. Builds static Linux binaries for amd64 and arm64, each on its own runner. `amgd` links SQLite statically against musl, so the binaries need no C library on the target. A step runs them in an empty `busybox` container to prove it.
3. Makes the macOS binaries for arm64 and amd64 in three jobs, so that no job both holds the signing keys and runs code from the build:
   - **Build macOS** builds them with a minimum macOS version (`macos_min` in `scripts/release/build-macos.sh`). It has no secrets.
   - **Sign and notarize macOS** signs them with the Developer ID identity and fixed identifiers (`SIGN_ID_PREFIX`), deletes the signing keychain, notarizes them, and packs them. It never builds and never runs the binaries. Only `codesign` may use the signing key.
   - **Verify macOS** checks the signatures and the notarization, and runs the binaries. It has no secrets.

   Each job seals what it hands on with `scripts/release/digest.sh` and passes the digest to the next job as a job output. The next job, and the release job, stop if a file does not match, so a file swapped between jobs is never signed or published.
4. Packs each platform as `agentmailguard_VERSION_OS_ARCH.tar.gz` with `amg`, `amgd`, `LICENSE`, `README.md`, `CHANGELOG.md`, `SECURITY.md`, the `docs` folder, and the agent skill in `skills` (`scripts/release/package.sh`). It writes `checksums.txt` and a [build provenance attestation](https://docs.github.com/en/actions/security-for-github-actions/using-artifact-attestations).
5. Fills in [the formula template](../packaging/homebrew/agentmailguard.rb) and checks it with `brew style` and `brew audit --strict` in a temporary tap (`scripts/release/check-formula.sh`).
6. Creates the GitHub release with the archives and `checksums.txt`.
7. Pushes the formula to `Formula/agentmailguard.rb` in [ivanrvpereira/homebrew-tap](https://github.com/ivanrvpereira/homebrew-tap), then waits until the tap's CI has installed and tested it on macOS (arm64 and Intel) and Linux (amd64 and arm64).

Every binary reports the tag through `amg --version` and `amgd --version`. The workflow sets it with the linker flag `-X github.com/ivanrvpereira/agentmailguard/internal/version.version=TAG`. A build without that flag reports the version that Go records from Git, or `dev`.

## Repository secrets

| Secret | Use |
|---|---|
| `DEVELOPER_ID_CERT_P12`, `DEVELOPER_ID_CERT_PASSWORD` | The Developer ID Application certificate with its private key, as a base64 `.p12`, and the `.p12` password. |
| `NOTARY_API_KEY_ID`, `NOTARY_API_ISSUER_ID`, `NOTARY_API_KEY_P8` | An App Store Connect **Team** API key for `notarytool`; individual keys cannot notarize. Store the raw `.p8` text, not base64. |
| `TAP_PUSH_TOKEN` | A fine-grained token with Contents read and write on `ivanrvpereira/homebrew-tap` only. Without it, the workflow prints the formula in its log, and you copy it to the tap by hand. |

## Do not change

- **The signing identity and identifiers.** macOS gives a Keychain item to the code that has the team ID and identifier of the program that created it. A new certificate team, or a new `SIGN_ID_PREFIX`, makes every macOS user approve `amgd`'s Keychain access again.
- **`macos_min` below the Go minimum.** When a new Go release needs a newer macOS, `scripts/release/build-macos.sh` fails with "raise macos_min". Raise it, and change the macOS version in [Install and build](install.md#install).

## Troubleshooting

- **Notarization fails with HTTP 403, "A required agreement is missing or has expired".** The Apple Developer account holder must accept the new agreement at developer.apple.com. HTTP 401 means a wrong key ID or issuer ID, or a key that is not a Team key.
- **The notary log reports issues.** The log is in the output of the "Notarize binaries" step.
- **`brew audit` fails.** Change the template in this repository, not the copy in the tap. To check it on your machine, fill it in with `scripts/release/render-formula.sh VERSION CHECKSUMS > /tmp/agentmailguard.rb`, then run `scripts/release/check-formula.sh /tmp/agentmailguard.rb`. The check uses a temporary tap and removes it.
- **The tap's CI fails after the push.** The release on GitHub is already public. Fix the template here, then publish a new patch version.
