package main

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func fixture[T any](t *testing.T, path string) T {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// Both original known answers must survive changes to the shared helpers.
func TestCommittedProfiles(t *testing.T) {
	t.Run("web", func(t *testing.T) {
		want := fixture[webVector](t, "../../web/src/testdata/authority/vectors.json")
		if !reflect.DeepEqual(generateWeb(), want) {
			t.Fatal("Web fixture changed")
		}
		verifyWeb(want)
	})
	t.Run("ios", func(t *testing.T) {
		want := fixture[iosVector](t, "../../ios/testdata/authority-v3.json")
		if !reflect.DeepEqual(generateIOS(), want) {
			t.Fatal("iOS fixture changed")
		}
		verifyIOS(want)
	})
}

func rejects(t *testing.T, verify func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Error("tampered signature accepted")
		}
	}()
	verify()
}

func corrupt(raw []byte) []byte {
	raw = bytes.Clone(raw)
	raw[len(raw)-1] ^= 1
	return raw
}

func TestRejectsTamperedSignatures(t *testing.T) {
	t.Run("web lease", func(t *testing.T) {
		v := generateWeb()
		v.Lease = corrupt(v.Lease)
		rejects(t, func() { verifyWeb(v) })
	})
	t.Run("web recovery", func(t *testing.T) {
		v := generateWeb()
		v.Recovery = corrupt(v.Recovery)
		rejects(t, func() { verifyWeb(v) })
	})
	t.Run("web approval", func(t *testing.T) {
		v := generateWeb()
		v.NormalApprovalSignature = corrupt(v.NormalApprovalSignature)
		rejects(t, func() { verifyWeb(v) })
	})
	t.Run("ios state", func(t *testing.T) {
		v := generateIOS()
		v.Entries[0].State = corrupt(v.Entries[0].State)
		rejects(t, func() { verifyIOS(v) })
	})
	t.Run("ios lease", func(t *testing.T) {
		v := generateIOS()
		v.Entries[0].Lease = corrupt(v.Entries[0].Lease)
		rejects(t, func() { verifyIOS(v) })
	})
	t.Run("ios recovery", func(t *testing.T) {
		v := generateIOS()
		last := len(v.Entries) - 1
		v.Entries[last].Proof = corrupt(v.Entries[last].Proof)
		rejects(t, func() { verifyIOS(v) })
	})
	t.Run("ios approval", func(t *testing.T) {
		v := generateIOS()
		v.ApprovalSignature = corrupt(v.ApprovalSignature)
		rejects(t, func() { verifyIOS(v) })
	})
}
