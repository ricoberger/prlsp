# prlsp

`prlsp` is an LSP server for surfacing GitHub PR review comments in-editor (as
diagnostics), with commands / code-actions to create replies and new comments.

## Install

### Releases

Check the [releases](https://github.com/ricoberger/prlsp/releases) page for the
full list of pre-built binaries.

1. Download the release for your os/arch
2. Unzip the archive to get the `prlsp` binary
3. Add the `prlsp` binary to your `PATH`

### Source

```sh
go install github.com/ricoberger/prlsp@latest
```

## Development

To build and run the binary the following commands can be used:

```sh
go build -o ./bin/prlsp .
./bin/prlsp
```

## Acknowledgments

This is a "fork" of [prlsp](https://github.com/toziegler/prlsp), which contains
the original code and some of the open PRs.
