//go:build darwin

package config

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestPhysicalMemoryMatchesSysctl(t *testing.T) {
	out, err := exec.Command("/usr/sbin/sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		t.Fatal(err)
	}
	want, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	got, err := PhysicalMemory()
	if err != nil || got != want {
		t.Errorf("PhysicalMemory() = %d, %v, want %d, nil (sysctl -n hw.memsize)", got, err, want)
	}
}
