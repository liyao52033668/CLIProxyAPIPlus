package util

import (
	"strings"
	"unsafe"

	"github.com/tidwall/gjson"
)

// JoinRawArrayBytes joins raw JSON fragments into a JSON array. It mirrors the
// array assembly used across translators without the translator common package.
func JoinRawArrayBytes(items [][]byte) []byte {
	if len(items) == 0 {
		return []byte(`[]`)
	}
	var b strings.Builder
	b.Grow(len(items) * 4)
	b.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(item)
	}
	b.WriteByte(']')
	return []byte(b.String())
}

// GetGJSONBytesNoCopy returns a GJSON result that may reference data directly.
// Callers must not retain the result or mutate data while using it.
func GetGJSONBytesNoCopy(data []byte, path string) gjson.Result {
	if len(data) == 0 {
		return gjson.Result{}
	}
	return gjson.Get(unsafe.String(unsafe.SliceData(data), len(data)), path)
}

// ParseGJSONBytesNoCopy parses data into a GJSON result that references data
// directly. gjson.ParseBytes copies the whole document, which is prohibitive
// for multi-megabyte payloads. Callers must not retain the result or mutate
// data while using it.
func ParseGJSONBytesNoCopy(data []byte) gjson.Result {
	if len(data) == 0 {
		return gjson.Result{}
	}
	return gjson.Parse(unsafe.String(unsafe.SliceData(data), len(data)))
}
