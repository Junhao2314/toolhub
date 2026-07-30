//go:build !linux

package main

import "fmt"

func main() {
	fmt.Println("toolhub-bridge is supported only on Linux")
}
