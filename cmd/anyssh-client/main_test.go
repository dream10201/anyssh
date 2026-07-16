package main

import (
	"testing"

	"anyssh/internal/bootstrap"
)

func TestBaseClientHasNoConfiguration(t *testing.T) {
	_, found, err := bootstrap.ReadExecutable()
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("test binary unexpectedly contains a client trailer")
	}
}
