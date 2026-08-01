package config

import (
	"bytes"
	"io"
)

// newReader is a tiny helper so Parse can feed a byte slice to yaml.NewDecoder, which wants an io.Reader.
func newReader(b []byte) io.Reader { return bytes.NewReader(b) }
