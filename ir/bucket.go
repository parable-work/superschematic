package ir

// DefaultContentMaxBytes is the largest object a content-addressed bucket
// puts when its Bucket service's config names no maxBytes (D142): 32 MiB.
// A content bucket reads a body whole to hash it, so the limit bounds what
// a put holds in memory.
const DefaultContentMaxBytes = 32 << 20
