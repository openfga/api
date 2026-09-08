package main

import (
	"strconv"
	"strings"
	"testing"
)

func TestParseArrayIndex(t *testing.T) {
	for _, test := range []struct {
		token  string
		length int
		want   int
	}{
		{token: "0", length: 1, want: 0},
		{token: "1", length: 2, want: 1},
		{token: "10", length: 11, want: 10},
	} {
		t.Run(test.token, func(t *testing.T) {
			got, err := parseArrayIndex(test.token, test.length)
			if err != nil {
				t.Fatalf("parseArrayIndex(%q, %d): %v", test.token, test.length, err)
			}
			if got != test.want {
				t.Fatalf("parseArrayIndex(%q, %d) = %d, want %d", test.token, test.length, got, test.want)
			}
		})
	}
}

func TestParseArrayIndexRejectsMalformedTokens(t *testing.T) {
	for _, test := range []struct {
		name   string
		token  string
		length int
	}{
		{name: "empty", token: "", length: 2},
		{name: "leading zero", token: "01", length: 2},
		{name: "multiple zeroes", token: "00", length: 2},
		{name: "positive sign", token: "+1", length: 2},
		{name: "negative sign", token: "-1", length: 2},
		{name: "partial numeric prefix", token: "1junk", length: 2},
		{name: "leading whitespace", token: " 1", length: 2},
		{name: "trailing whitespace", token: "1 ", length: 2},
		{
			name:   "int overflow",
			token:  strconv.FormatUint(uint64(^uint(0)>>1)+1, 10),
			length: 2,
		},
		{name: "overflow", token: strings.Repeat("9", 100), length: 2},
		{name: "out of range", token: "2", length: 2},
		{name: "zero in empty array", token: "0", length: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseArrayIndex(test.token, test.length); err == nil {
				t.Fatalf("parseArrayIndex(%q, %d) succeeded", test.token, test.length)
			}
		})
	}
}
