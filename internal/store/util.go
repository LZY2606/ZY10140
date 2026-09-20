package store

import "crypto/sha256"

func sha256sum(b []byte) [32]byte { return sha256.Sum256(b) }
