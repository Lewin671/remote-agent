package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"

	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

var errV3Acceptance = errors.New("v3_acceptance_corrupt")

// The offline tool reads exactly the generation-3 checksum format. Runtime
// endpoints continue to support only v4; no old request is copied into v4.
func v3AcceptedChecksum(key []byte, deadline uint64, done int, result []byte, failed int) []byte {
	body := binary.BigEndian.AppendUint32(nil, uint32(len(key)))
	body = append(body, key...)
	body = binary.BigEndian.AppendUint64(body, deadline)
	body = binary.BigEndian.AppendUint32(body, uint32(done))
	body = binary.BigEndian.AppendUint32(body, uint32(len(result)))
	body = append(body, result...)
	body = binary.BigEndian.AppendUint32(body, uint32(failed))
	hash := sha256.New()
	hash.Write([]byte("ra3/accepted-entry\x00"))
	hash.Write(body)
	return hash.Sum(nil)
}

// Do not trust even MAX(deadline) until every acceptance row has passed its
// original checksum and shape checks. SQLite integrity_check cannot do this.
func validatedV3Deadline(db *sql.DB) (uint64, error) {
	rows, err := db.Query(`SELECT key,deadline,done,result,failed,checksum FROM accepted`)
	if err != nil {
		return 0, errV3Acceptance
	}
	defer rows.Close()
	var latest uint64
	for rows.Next() {
		var key, result, checksum []byte
		var deadline int64
		var done, failed int
		if err := rows.Scan(&key, &deadline, &done, &result, &failed, &checksum); err != nil || len(key) > 1024 || deadline <= 0 || done < 0 || done > 1 || failed < 0 || failed > 1 || len(result) > deviceauth.MaxCipherBytes || len(checksum) != sha256.Size {
			return 0, errV3Acceptance
		}
		if !bytes.Equal(checksum, v3AcceptedChecksum(key, uint64(deadline), done, result, failed)) {
			return 0, errV3Acceptance
		}
		latest = max(latest, uint64(deadline))
	}
	if rows.Err() != nil {
		return 0, errV3Acceptance
	}
	return latest, nil
}
