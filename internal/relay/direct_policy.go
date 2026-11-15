package relay

import (
	"encoding/json"
	"io"
	"os"
	"sync"
	"sync/atomic"

	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/transport/peerwire"
)

type directSetting struct {
	Revision uint64 `json:"revision"`
	Enabled  bool   `json:"enabled"`
}
type directPolicyData struct {
	Version int                      `json:"version"`
	Machine directSetting            `json:"direct_enabled"`
	Peers   map[string]directSetting `json:"peers"`
}
type directPolicyStore struct {
	snapshot atomic.Pointer[directPolicyData]
	mu       sync.Mutex
	path     string
	data     directPolicyData
	save     func(string, []byte) error
}

func openDirectPolicy(path string) (*directPolicyStore, error) {
	p := &directPolicyStore{path: path, data: directPolicyData{Version: 1, Machine: directSetting{Enabled: true}, Peers: map[string]directSetting{}}, save: writeFileSync}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		initial := p.data
		p.snapshot.Store(&initial)
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 32769))
	if err != nil {
		return nil, err
	}
	if len(raw) > 32768 {
		return nil, deviceauth.ErrStorage
	}
	var data directPolicyData
	if json.Unmarshal(raw, &data) != nil || data.Version != 1 || data.Peers == nil || len(data.Peers) > deviceauth.MaxMembers {
		return nil, deviceauth.ErrStorage
	}
	for id := range data.Peers {
		if len(id) != 32 {
			return nil, deviceauth.ErrStorage
		}
	}
	p.data = data
	p.snapshot.Store(&data)
	return p, nil
}
func (p *directPolicyStore) enabled(peer string) bool {
	data := p.snapshot.Load()
	if data == nil {
		return false
	}
	v, exists := data.Peers[peer]
	return data.Machine.Enabled && (!exists || v.Enabled)
}
func (p *directPolicyStore) commit(data directPolicyData) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if err = p.save(p.path, raw); err != nil {
		return err
	}
	p.data = data
	p.snapshot.Store(&data)
	return nil
}

// apply is called only after device-pair authentication against the current
// head. Roles come from that head; policy payloads cannot name an authority.
func (p *directPolicyStore) apply(o peerwire.Opened, authority string) ([]byte, error) {
	if o.Type != 9 || o.Context.Class != 2 || o.Context.FromRole != 1 || o.Context.ToRole != 2 {
		return nil, deviceauth.ErrUntrusted
	}
	if _, err := peerwire.KnownPayload(2, 9, o.Payload); err != nil {
		return nil, err
	}
	f, err := peerwire.ParseMessage(o.Payload, "DirectPolicy")
	if err != nil {
		return nil, err
	}
	scope, revision, enabled := f[4].U, f[2].U, f[3].U == 1
	peer := string(f[1].B)
	if scope == 1 && string(o.Context.From) != authority || scope == 2 && (peer != string(o.Context.From) || enabled) {
		return nil, deviceauth.ErrUntrusted
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	current := p.data.Machine
	if scope == 2 {
		var exists bool
		current, exists = p.data.Peers[peer]
		if !exists {
			current = directSetting{Enabled: true}
		}
	}
	applied := false
	if revision == current.Revision && enabled == current.Enabled {
		applied = true
	}
	if revision > current.Revision {
		data := p.data
		data.Peers = make(map[string]directSetting, len(p.data.Peers))
		for id, v := range p.data.Peers {
			data.Peers[id] = v
		}
		updated := directSetting{revision, enabled}
		if scope == 1 {
			data.Machine = updated
		} else {
			if _, exists := data.Peers[peer]; !exists && len(data.Peers) >= deviceauth.MaxMembers {
				return nil, deviceauth.ErrCapacity
			}
			data.Peers[peer] = updated
		}
		if err = p.commit(data); err != nil {
			return nil, err
		}
		current = updated
		applied = true
	}
	yes, value := uint64(0), uint64(0)
	if applied {
		yes = 1
	}
	if current.Enabled {
		value = 1
	}
	return peerwire.PB(peerwire.U(1, revision), peerwire.U(2, yes), peerwire.U(3, value), peerwire.U(4, o.Context.Seq), peerwire.B(5, o.Hash), peerwire.U(6, scope), peerwire.B(7, f[1].B)), nil
}

// setLocal is the persistent machine setting. Local changes advance the same
// revision as authenticated administrator updates; an old remote request cannot
// silently undo a newer local choice.
func (p *directPolicyStore) setLocal(enabled bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.data.Machine.Revision == ^uint64(0) {
		return deviceauth.ErrCapacity
	}
	data := p.data
	data.Machine = directSetting{p.data.Machine.Revision + 1, enabled}
	return p.commit(data)
}

type directPolicyVersion struct{ machine, peer uint64 }

func (p *directPolicyStore) version(peer string) directPolicyVersion {
	data := p.snapshot.Load()
	if data == nil {
		return directPolicyVersion{}
	}
	return directPolicyVersion{data.Machine.Revision, data.Peers[peer].Revision}
}
func (p *directPolicyStore) matches(peer string, want directPolicyVersion) bool {
	data := p.snapshot.Load()
	if data == nil {
		return false
	}
	v, exists := data.Peers[peer]
	return data.Machine.Enabled && (!exists || v.Enabled) && data.Machine.Revision == want.machine && v.Revision == want.peer
}
