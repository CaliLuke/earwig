# Repository Instructions

## Releases

After you publish a release, update the local Homebrew installation to that release. Complete the full update process:

1. Run `brew update`.
2. Run `brew upgrade caliluke/tap/earwig`.
3. Restart the service with `brew services restart caliluke/tap/earwig`.
4. Confirm that `earwig version` reports the new release.
5. Confirm that the Homebrew service is running.
6. Run `earwig status` and `earwig doctor`.

Treat installation, upgrade, service, and database errors as release blockers. Diagnose and fix them before you consider the release complete.
