package config

import (
	"encoding/binary"
	"fmt"
	"syscall"
)

// PhysicalMemory returns the machine's physical RAM in bytes, from sysctl(3)'s hw.memsize, for
// editor_ram_budget_gb's default (docs/contracts.md (h) RAM budget).
func PhysicalMemory() (uint64, error) {
	s, err := syscall.Sysctl("hw.memsize")
	if err != nil {
		return 0, fmt.Errorf("sysctl hw.memsize: %w", err)
	}
	b := []byte(s)
	// Sysctl drops a final NUL byte as a string's terminator. darwin is little-endian on arm64
	// and amd64 alike, so for this uint64 that byte is the most significant, and a zero.
	if len(b) == 7 {
		b = append(b, 0)
	}
	if len(b) != 8 {
		return 0, fmt.Errorf("sysctl hw.memsize: got %d bytes, want 8", len(b))
	}
	return binary.LittleEndian.Uint64(b), nil
}
