// Package docs carries the docs pages a binary prints into it:
// `superschematic catalog capabilities` prints the capability map.
package docs

import _ "embed"

// CapabilityMap is src/content/docs/start/capabilities.md, frontmatter and
// all.
//
//go:embed src/content/docs/start/capabilities.md
var CapabilityMap []byte
