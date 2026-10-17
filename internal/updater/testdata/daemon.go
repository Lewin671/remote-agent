// A real subprocess for the updater's replacement/restart/recovery test.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

var version = "1.1.0"

func main() {
	exe, _ := os.Executable()
	h := map[string]any{"format": 1, "version": version, "channel": "release", "protocol": 6, "state": 1, "pid": os.Getpid(), "executable": exe, "idleSince": time.Now().Add(-time.Minute)}
	if len(os.Args) == 2 && os.Args[1] == "update-build-info" {
		_ = json.NewEncoder(os.Stdout).Encode(h)
		return
	}
	if len(os.Args) != 3 || os.Args[1] != "daemon" {
		os.Exit(2)
	}
	if version == "1.2.0" && os.Getenv("UPDATE_HELPER_FAIL_NEW") == "1" {
		os.Exit(3)
	}
	_ = os.Remove(os.Args[2])
	ln, err := net.Listen("unix", os.Args[2])
	if err != nil {
		os.Exit(4)
	}
	fmt.Println("ready")
	mux := http.NewServeMux()
	mux.HandleFunc("/update/health", func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(h) })
	_ = http.Serve(ln, mux)
}
