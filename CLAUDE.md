# fog-agent

The FOG Project's host agent, in Go. The server side lives in
`FOGProject/fogproject`.

## Build and test

These are the commands CI runs (`.github/workflows/ci.yml`):

    gofmt -l .                       # must print nothing
    go vet ./...
    GOOS=windows go vet ./...
    GOOS=darwin go vet ./...
    go test ./... -count=1
    go build -o bin/fog-agent ./cmd/fog-agent

`build/cross.sh` builds every target into `dist/`. `build/msi.sh` builds the
Windows installer. `docs/RELEASING.md` gives the release steps.

## Branches and merges

- This repo works on `main`. A pull request merges by squash only.
- **Server work for this agent goes to fogproject `dev-branch`.** FOG 1.6.0
  is released, so `dev-branch` is where development happens. `working-1.6`
  is retired. Base a fogproject branch on `origin/dev-branch` and open its
  pull request against `dev-branch`.

## Design

Each capability has a numbered design in `docs/design/`. The wire protocol
is `docs/design/protocol-v1.md`. A change that alters what a design says
updates that design in the same pull request.
