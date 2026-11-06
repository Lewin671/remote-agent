package e2e

// AccountDeletionMessage binds an irreversible account deletion to the current
// membership head and a server-issued, single-use, one-minute challenge.
func AccountDeletionMessage(account string, head, challenge []byte) []byte {
	b := []byte("remote-agent/v1/delete-account\x00" + account + "\x00")
	b = append(b, head...)
	return append(b, challenge...)
}
