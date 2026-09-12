package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"testing"
)

func gunzipForTest(t *testing.T, input []byte) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	result, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
