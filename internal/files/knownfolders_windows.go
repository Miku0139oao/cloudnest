//go:build windows

package files

import (
	"syscall"
	"unicode/utf16"
	"unsafe"
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var folders = map[string]guid{
	"desktop":   {0xB4BFCC3A, 0xDB2C, 0x424C, [8]byte{0xB0, 0x29, 0x7F, 0xE9, 0x9A, 0x87, 0xC6, 0x41}},
	"documents": {0xFDD39AD0, 0x238F, 0x46AF, [8]byte{0xAD, 0xB4, 0x6C, 0x85, 0x48, 0x03, 0x69, 0xC7}},
	"pictures":  {0x33E28130, 0x4E1E, 0x4676, [8]byte{0x83, 0x5A, 0x98, 0x39, 0x5C, 0x3B, 0xC3, 0xBB}},
	"downloads": {0x374DE290, 0x123F, 0x4565, [8]byte{0x91, 0x64, 0x39, 0xC4, 0x92, 0x5E, 0x46, 0x7B}},
	"music":     {0x4BD8D571, 0x6D19, 0x48D3, [8]byte{0xBE, 0x97, 0x42, 0x22, 0x20, 0x08, 0x0E, 0x43}},
}

var (
	shell32                  = syscall.NewLazyDLL("shell32.dll")
	ole32                    = syscall.NewLazyDLL("ole32.dll")
	procSHGetKnownFolderPath = shell32.NewProc("SHGetKnownFolderPath")
	procCoTaskMemFree        = ole32.NewProc("CoTaskMemFree")
)

func knownFolder(id string) (string, error) {
	g, ok := folders[id]
	if !ok {
		return "", syscall.ERROR_FILE_NOT_FOUND
	}
	var p *uint16
	hr, _, _ := procSHGetKnownFolderPath.Call(
		uintptr(unsafe.Pointer(&g)), 0, 0, uintptr(unsafe.Pointer(&p)),
	)
	if hr != 0 || p == nil {
		return "", syscall.Errno(hr)
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
	return utf16PtrToString(p), nil
}

func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	n := 0
	for ptr := unsafe.Pointer(p); *(*uint16)(ptr) != 0; ptr = unsafe.Add(ptr, 2) {
		n++
	}
	return string(utf16.Decode(unsafe.Slice(p, n)))
}
