package main

import _ "embed"

// version is the build version, overridable via -ldflags "-X main.version=...".
var version = "dev"

// Scaffold/build files, embedded from templates/. They are real files (editable
// with full tooling). `sbx build` uses them for the shared base image, and
// `sbx init` copies them into .sbx/ as a per-project override.

//go:embed templates/Dockerfile
var dockerfileTemplate string

//go:embed templates/entrypoint.sh
var entrypointTemplate string
