// Command fakebin stands in for an anvil binary in Preflight tests.
package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	switch os.Getenv("FAKEBIN_MODE") {
	case "fail":
		fmt.Fprintln(os.Stderr, "Error: "+strings.Repeat("x", 1000))
		os.Exit(1)
	case "echo":
		wd, _ := os.Getwd()
		handoff, ok := os.LookupEnv("ANVIL_RELOAD_HANDOFF")
		if !ok {
			handoff = "unset"
		}
		fmt.Printf("args=%s wd=%s handoff=%s\n", strings.Join(os.Args[1:], " "), wd, handoff)
	default:
		fmt.Println("v9.9.9-fake")
		fmt.Println("second line")
	}
}
