// authorityserver runs the actual Go relay in an isolated temporary database
// for Web workflow tests. It has no credentials or production configuration.
package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"

	"github.com/Lewin671/remote-agent/internal/server"
)

func main() {
	directory, err := os.MkdirTemp("", "ra-authority-server-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "relay.db")
	config := server.Config{DBPath: path, SetupToken: "test-setup", Signup: server.SignupToken, SignupBits: 4, MaxAccounts: 100}
	if len(os.Args) > 1 && os.Args[1] == "open" {
		config.Signup = server.SignupOpen
	}
	relay, err := server.New(config)
	if err != nil {
		panic(err)
	}
	defer relay.Close()
	http := httptest.NewServer(relay.Handler())
	defer http.Close()
	fmt.Println(http.URL)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var command struct {
			Op         string
			RecoveryID string
		}
		if err := json.Unmarshal(scanner.Bytes(), &command); err != nil {
			panic(err)
		}
		// Only the waiting-row timestamp is advanced, deliberately in the fixture,
		// to test completion without changing the production 48-hour rule.
		var result error
		switch command.Op {
		case "ready":
			db, err := sql.Open("sqlite", path)
			if err != nil {
				panic(err)
			}
			_, result = db.Exec("UPDATE security_recovery SET ready_ms=1 WHERE recovery_id=?", command.RecoveryID)
			db.Close()
		default:
			result = fmt.Errorf("unknown fixture command")
		}
		if result != nil {
			fmt.Println(`{"ok":false}`)
		} else {
			fmt.Println(`{"ok":true}`)
		}
	}
}
