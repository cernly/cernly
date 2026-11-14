// Package store holds the database handle.
package store

type DB struct{}

func Open() *DB { return &DB{} }
