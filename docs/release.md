# Release process

Releases are built and published by GitHub Actions when a `v*` tag is pushed.

1. Confirm that `main` is clean and current.
2. Run `go test -race ./...` and `go vet ./...`.
3. Create and push an annotated semantic-version tag:

   ```sh
   git tag -a v1.1.0 -m "cx v1.1.0"
   git push origin v1.1.0
   ```

The release workflow builds Linux and macOS binaries for amd64 and arm64,
injects the tag version into `cx --version`, generates SHA-256 checksums, and
publishes the assets to a GitHub Release.

Do not move or overwrite a published release tag. Create a new patch release
for corrections.
