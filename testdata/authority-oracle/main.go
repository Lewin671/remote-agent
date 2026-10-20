// Shared test-only authority oracle. Profiles retain the original Web and iOS
// fixtures, including their distinct fixed keys, clocks, state chains and wire formats.
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintln(os.Stderr, r)
			os.Exit(1)
		}
	}()
	if len(os.Args) < 2 || len(os.Args) > 3 || (len(os.Args) == 3 && os.Args[2] != "verify") {
		panic("usage: authority-oracle web|ios [verify]")
	}
	verifying := len(os.Args) == 3
	var result any
	switch os.Args[1] {
	case "web":
		if verifying {
			var v webVector
			check(json.NewDecoder(os.Stdin).Decode(&v))
			verifyWeb(v)
		} else {
			result = generateWeb()
		}
	case "ios":
		if verifying {
			var v iosVector
			check(json.NewDecoder(os.Stdin).Decode(&v))
			verifyIOS(v)
		} else {
			v := generateIOS()
			verifyIOS(v)
			result = v
		}
	default:
		panic("unknown authority fixture profile")
	}
	if verifying {
		fmt.Println("verified: states, leases, recovery, approvals, grants and PrevSecret")
		return
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	check(encoder.Encode(result))
}
