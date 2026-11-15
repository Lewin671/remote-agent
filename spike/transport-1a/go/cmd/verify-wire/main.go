package main

import (
	"example.com/transport-sdk-lab/labwire"
	"fmt"
	"os"
	"strings"
)

func main() {
	if !strings.HasPrefix(os.Getenv("REMOTE_AGENT_HOME"), "/tmp/") {
		panic("isolation")
	}
	raw, e := os.ReadFile(os.Args[1])
	if e != nil {
		panic(e)
	}
	a := labwire.Fixture("c000000000000001", 1)
	b := labwire.Fixture("m000000000000001", 2)
	v, e := labwire.Open(raw, a.Public(), b, 1700000000000)
	if e != nil {
		panic(e)
	}
	if v.Type != 3 || string(v.Payload) != "raw SDP remains byte-exact\r\n" {
		panic("payload")
	}
	fmt.Println("Go verified native-generated packet")
}
