
package delta

import (
	"crypto/md5"
	"crypto/sha256"
	"hash"
)

func newMD5() hash.Hash    { return md5.New() }
func newSHA256() hash.Hash { return sha256.New() }
