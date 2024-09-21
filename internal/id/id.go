package id

import (
	"github.com/nrednav/cuid2"
)

var Generate func() string

func init() {
	var err error
	Generate, err = cuid2.Init(
		cuid2.WithLength(32),
	)
	if err != nil {
		panic(err)
	}
}
