# Releasing Earwig

Earwig releases are native bundles containing the Go daemon and a compiled
Claude reader. GitHub Actions builds on native macOS and Linux runners for
`amd64` and `arm64`, publishes checksums and build-provenance attestations, and
generates the Homebrew formula.

## One-time repository setup

Earwig is distributed under the MIT License. Release archives include the
repository's `LICENSE` file.

Add a write-enabled deploy key to `CaliLuke/homebrew-tap`, then add its private
key to `CaliLuke/earwig` as an Actions secret named
`HOMEBREW_TAP_DEPLOY_KEY`. The release fails if this secret is unavailable so
the GitHub release and Homebrew tap cannot silently drift apart.

## Release checklist

1. Run `./scripts/verify`, `./scripts/verify-security`,
   `./scripts/verify-live`, and `./scripts/verify-distribution`.
2. Confirm the working tree is clean and the release commit is on `main`.
3. Create and push a semantic-version tag, for example:

   ```sh
   git tag -s v0.1.0 -m "Earwig v0.1.0"
   git push origin v0.1.0
   ```

4. Wait for the Release workflow to finish. Verify four archives, individual
   checksum files, `checksums.txt`, `earwig.rb`, and attestations are attached.
5. Install from the tap on a clean machine and run `earwig doctor`:

   ```sh
   brew tap caliluke/tap
   brew install earwig
   earwig doctor
   brew services start earwig
   ```

The workflow uses immutable commit pins for third-party Actions. Update the
pin and its adjacent version comment together.
