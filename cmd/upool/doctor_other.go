//go:build !darwin

package main

import (
	"context"
	"errors"
)

// cloneSharingCheck does not run off macOS, where there is no clonefile(2) to check.
func cloneSharingCheck(context.Context, string) (doctorCheck, bool) { return doctorCheck{}, false }

// desktopHelpers returns no paths off macOS, where the desktop app has no helper that runs its
// create hooks under a folder macOS protects, so the documents-access check does not run.
func desktopHelpers(string) []string { return nil }

// networkVolume returns an error wrapping errors.ErrUnsupported: network volumes are detected
// through macOS's statfs flags.
func networkVolume(string) (string, bool, error) { return "", false, errors.ErrUnsupported }
