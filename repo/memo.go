package repo

import (
	"crypto/sha256"
	"sync"
)

// memo holds values computed once per repo view. One view serves one scan, so
// nothing cached here outlives it; a process may run many scans.
type memo struct {
	mu sync.Mutex
	m  map[any]any
}

// Memo returns the value stored under key, computing and storing it on the
// first call. Readers shared by plugins (jsread, pyread) cache per scan this
// way. compute runs outside the lock; a racing duplicate is discarded, so
// every caller sees the first stored value.
func (r *Repo) Memo(key any, compute func() any) any {
	r.memo.mu.Lock()
	v, ok := r.memo.m[key]
	r.memo.mu.Unlock()
	if ok {
		return v
	}
	v = compute()
	r.memo.mu.Lock()
	defer r.memo.mu.Unlock()
	if old, ok := r.memo.m[key]; ok {
		return old
	}
	if r.memo.m == nil {
		r.memo.m = map[any]any{}
	}
	r.memo.m[key] = v
	return v
}

type memoFileKey[T any] [32]byte

type memoFileVal[T any] struct {
	v   T
	err error
}

// MemoFile caches read for the scan, by file path and content, since several
// plugins read every file. T keeps the readers' results apart.
func MemoFile[T any](r *Repo, file string, src []byte, read func() (T, error)) (T, error) {
	key := memoFileKey[T](sha256.Sum256(append([]byte(file+"\x00"), src...)))
	c := r.Memo(key, func() any {
		v, err := read()
		return memoFileVal[T]{v, err}
	}).(memoFileVal[T])
	return c.v, c.err
}
