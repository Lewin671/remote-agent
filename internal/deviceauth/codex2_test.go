package deviceauth

import (
	"fmt"
	"testing"
	"time"
)

func TestPersistedFloorTimestampThrottlesTouchAndAccept(t *testing.T) {
	for _, mode := range []string{"touch", "accept"} {
		t.Run(mode, func(t *testing.T) {
			c := newCurrentChain(t)
			m := c.machineEndpoint(t, "")
			defer m.Close()
			cl := c.clientEndpoint(t)
			defer cl.Close()
			if _, err := m.db.Exec(`CREATE TABLE floor_writes (ms INTEGER); CREATE TRIGGER count_floor AFTER UPDATE OF floor_ms ON security BEGIN INSERT INTO floor_writes VALUES(NEW.floor_ms); END`); err != nil {
				t.Fatal(err)
			}
			now := uint64(time.Now().UnixMilli())
			for i := 0; i < 10; i++ {
				at := now + uint64(i)*10
				if mode == "touch" {
					if err := m.Touch(at); err != nil {
						t.Fatal(err)
					}
				} else {
					w, err := cl.Prepare(Header{Purpose: Command, Kind: 5, Machine: c.machine.ID, Request: fmt.Sprintf("floor-request-%016d", i), Deadline: now + 60000}, nil, commandBody(t))
					if err != nil {
						t.Fatal(err)
					}
					if _, err := m.Accept(w, nil, at, func(Header, []byte) ([]byte, error) { return nil, nil }); err != nil {
						t.Fatal(err)
					}
				}
			}
			var writes int
			if err := m.db.QueryRow(`SELECT count(*) FROM floor_writes`).Scan(&writes); err != nil {
				t.Fatal(err)
			}
			if writes != 1 || m.data.FloorSaved != now {
				t.Fatalf("floor was flushed %d times; saved=%d want %d", writes, m.data.FloorSaved, now)
			}
			if err := m.Touch(now + 1000); err != nil {
				t.Fatal(err)
			}
			if err := m.db.QueryRow(`SELECT count(*) FROM floor_writes`).Scan(&writes); err != nil {
				t.Fatal(err)
			}
			if writes != 2 {
				t.Fatalf("next interval did not flush: %d", writes)
			}
		})
	}
}
