# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `upool --version` prints the version. Any other invocation prints usage to stderr and exits 1;
  until now every invocation printed the version and exited 0.
- `internal/paths`: `Key`, the canonical form every path upool compares goes through (absolute,
  symlinks resolved on the longest existing prefix, cleaned, lowercased on a case-insensitive
  volume), and `Within` for comparing keys. Nothing calls it yet.
