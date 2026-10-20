//go:build !darwin

package main

import (
	"context"
	"errors"
)

// cloneSharingCheck does not run off macOS, where there is no clonefile(2) to check.
func cloneSharingCheck(context.Context, string) (doctorCheck, bool) { return doctorCheck{}, false }

// networkVolume returns an error wrapping errors.ErrUnsupported: network volumes are detected
// through macOS's statfs flags.
func networkVolume(string) (string, bool, error) { return "", false, errors.ErrUnsupported }
