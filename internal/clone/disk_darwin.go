package clone

import (
	"encoding/binary"
	"fmt"
	"syscall"
	"unsafe"
)

// privateSizes is whether this platform reports private sizes: APFS does.
const privateSizes = true

// The getattrlist(2) constants privateSize uses, from <sys/attr.h>.
const (
	attrBitMapCount = 5
	// attrCmnExtPrivateSize is ATTR_CMNEXT_PRIVATESIZE, asked for in the fork attributes.
	attrCmnExtPrivateSize = 0x00000008
	// fsoptNoFollow is FSOPT_NOFOLLOW, and fsoptAttrCmnExtended FSOPT_ATTR_CMN_EXTENDED, which
	// makes the fork attributes mean the extended common ones.
	fsoptNoFollow        = 0x00000001
	fsoptAttrCmnExtended = 0x00000020
)

// attrList is struct attrlist.
type attrList struct {
	bitmapCount uint16
	reserved    uint16
	commonAttr  uint32
	volAttr     uint32
	dirAttr     uint32
	fileAttr    uint32
	forkAttr    uint32
}

// privateSize returns the APFS private size of the file at path, without following a symbolic
// link: getattrlist(2) with ATTR_CMNEXT_PRIVATESIZE, which the probe behind A32 read with the same
// call (docs/evidence/apfs-private-size/probe.txt). The reply is a 4-byte length and an 8-byte
// off_t, packed, in the machine's byte order, little-endian on every Mac upool runs on. The call
// goes through the syscall package, so it needs no dependency.
func privateSize(path string) (int64, error) {
	p, err := syscall.BytePtrFromString(path)
	if err != nil {
		return 0, err
	}
	req := attrList{bitmapCount: attrBitMapCount, forkAttr: attrCmnExtPrivateSize}
	var reply [12]byte
	_, _, errno := syscall.Syscall6(syscall.SYS_GETATTRLIST, uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&req)), uintptr(unsafe.Pointer(&reply[0])), uintptr(len(reply)),
		fsoptNoFollow|fsoptAttrCmnExtended, 0)
	if errno != 0 {
		return 0, fmt.Errorf("reading the private size of %s: %w", path, errno)
	}
	return int64(binary.LittleEndian.Uint64(reply[4:])), nil
}
