//go:build !(linux && amd64)

package main

var bundledLibs = map[string][]byte{}
