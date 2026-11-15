//go:build wasip1

package main

import "unsafe"

//go:wasmimport wasi_snapshot_preview1 fd_write
func fdWrite(fd int32, iovs unsafe.Pointer, n int32, written unsafe.Pointer) int32

//go:wasmimport wasi_snapshot_preview1 poll_oneoff
func pollOneoff(in, out unsafe.Pointer, nsubs int32, nevents unsafe.Pointer) int32

// hugeCounts calls fd_write and poll_oneoff with counts far past memory and
// returns their errnos: the host must refuse them, not allocate for them.
func hugeCounts() (int32, int32) {
	var buf [64]byte
	var n uint32
	w := fdWrite(1, unsafe.Pointer(&buf[0]), 1<<31-1, unsafe.Pointer(&n))
	p := pollOneoff(unsafe.Pointer(&buf[0]), unsafe.Pointer(&buf[0]), 1<<31-1, unsafe.Pointer(&n))
	return w, p
}
