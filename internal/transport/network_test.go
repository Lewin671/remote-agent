package transport

import (
	"context"
	"crypto/sha256"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestNetworkObservationsCoalesceAndNoticeSameInterfaceAddressChanges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		hints := make(chan struct{})
		ticks := make(chan time.Time)
		scanned := make(chan struct{})
		var snapshot atomic.Value
		snapshot.Store(sha256.Sum256([]byte("interface-7/address-old")))
		var stopped atomic.Bool
		out := observeNetworkChanges(ctx, hints, ticks, func() [32]byte {
			value := snapshot.Load().([32]byte)
			scanned <- struct{}{}
			return value
		}, func() { stopped.Store(true) })
		<-scanned
		synctest.Wait()
		for range 1000 {
			hints <- struct{}{}
			<-scanned
		}
		synctest.Wait()
		select {
		case <-out:
			t.Fatal("duplicate hints advanced generation")
		default:
		}
		snapshot.Store(sha256.Sum256([]byte("interface-7/address-new")))
		hints <- struct{}{}
		<-scanned
		if generation := <-out; generation != 1 {
			t.Fatalf("same-interface change: %d", generation)
		}
		for n := 2; n <= 5; n++ {
			snapshot.Store(sha256.Sum256([]byte{byte(n)}))
			ticks <- time.Now()
			<-scanned
		}
		synctest.Wait()
		if generation := <-out; generation != 5 {
			t.Fatalf("did not retain newest generation: %d", generation)
		}
		cancel()
		synctest.Wait()
		if _, ok := <-out; ok || !stopped.Load() {
			t.Fatal("network observer leaked")
		}
	})
}

func TestFingerprintIgnoresNonDefaultInterfacesAndDetectsRouteOrDNSChanges(t *testing.T) {
	en0 := net.Interface{Index: 4, Name: "en0", Flags: net.FlagUp | net.FlagMulticast}
	awdl0 := net.Interface{Index: 9, Name: "awdl0", Flags: net.FlagUp | net.FlagMulticast}
	llw0 := net.Interface{Index: 10, Name: "llw0", Flags: net.FlagUp}
	utun5 := net.Interface{Index: 15, Name: "utun5", Flags: net.FlagUp | net.FlagPointToPoint}
	docker0 := net.Interface{Index: 20, Name: "docker0", Flags: net.FlagUp}
	veth9 := net.Interface{Index: 21, Name: "veth9", Flags: net.FlagUp}

	addrs := map[int][]net.Addr{
		4:  {&net.IPNet{IP: net.ParseIP("192.0.2.10"), Mask: net.CIDRMask(24, 32)}},
		9:  {&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)}},
		10: {&net.IPNet{IP: net.ParseIP("fe80::2"), Mask: net.CIDRMask(64, 128)}},
		15: {&net.IPNet{IP: net.ParseIP("10.8.0.2"), Mask: net.CIDRMask(24, 32)}},
		20: {&net.IPNet{IP: net.ParseIP("172.17.0.1"), Mask: net.CIDRMask(16, 32)}},
		21: {&net.IPNet{IP: net.ParseIP("172.17.0.99"), Mask: net.CIDRMask(16, 32)}},
	}
	lookup := func(i net.Interface) ([]net.Addr, error) { return addrs[i.Index], nil }
	base := networkSnapshot{
		interfaces: []net.Interface{en0, awdl0},
		addrs:      lookup,
		routes:     []defaultRoute{{IfIndex: 4, Family: "v4", Gateway: "192.0.2.1"}},
		dnsLines:   []string{"dns:nameserver 192.0.2.53"},
	}
	want := fingerprintSnapshot(base)

	noisy := base
	noisy.interfaces = []net.Interface{en0, awdl0, llw0, utun5, docker0, veth9}
	if got := fingerprintSnapshot(noisy); got != want {
		t.Fatal("non-default interfaces changed network fingerprint")
	}
	addrs[9] = append(addrs[9], &net.IPNet{IP: net.ParseIP("fe80::99"), Mask: net.CIDRMask(64, 128)})
	if got := fingerprintSnapshot(noisy); got != want {
		t.Fatal("non-default interface address churn changed network fingerprint")
	}

	changedIP := base
	changedIP.addrs = func(i net.Interface) ([]net.Addr, error) {
		if i.Index == 4 {
			return []net.Addr{&net.IPNet{IP: net.ParseIP("192.0.2.20"), Mask: net.CIDRMask(24, 32)}}, nil
		}
		return addrs[i.Index], nil
	}
	if fingerprintSnapshot(changedIP) == want {
		t.Fatal("default interface address change did not change fingerprint")
	}

	changedGW := base
	changedGW.routes = []defaultRoute{{IfIndex: 4, Family: "v4", Gateway: "192.0.2.254"}}
	if fingerprintSnapshot(changedGW) == want {
		t.Fatal("default gateway change did not change fingerprint")
	}

	resolv := filepath.Join(t.TempDir(), "resolv.conf")
	if err := os.WriteFile(resolv, []byte("# comment\nsearch corp.example\nnameserver 192.0.2.53\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dnsA := base
	dnsA.dnsLines = dnsFingerprintLines(resolv)
	if err := os.WriteFile(resolv, []byte("search corp.example\nnameserver 192.0.2.54\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dnsB := base
	dnsB.dnsLines = dnsFingerprintLines(resolv)
	if fingerprintSnapshot(dnsA) == fingerprintSnapshot(dnsB) {
		t.Fatal("DNS configuration change did not change fingerprint")
	}
}

func TestRouteAvailableSnapshot(t *testing.T) {
	en0 := net.Interface{Index: 4, Name: "en0", Flags: net.FlagUp}
	candidateIP := net.ParseIP("192.0.2.10")
	snap := networkSnapshot{
		interfaces: []net.Interface{en0},
		addrs: func(i net.Interface) ([]net.Addr, error) {
			if i.Index == 4 {
				return []net.Addr{&net.IPNet{IP: candidateIP, Mask: net.CIDRMask(24, 32)}}, nil
			}
			return nil, nil
		},
		routes: []defaultRoute{{IfIndex: 4, Family: "v4", Gateway: "192.0.2.1"}},
	}
	if !routeAvailableSnapshot(snap, candidateIP) {
		t.Fatal("expected IPv4 route on UP default interface to remain available")
	}
	if !routeAvailableSnapshot(snap, net.ParseIP("127.0.0.1")) {
		t.Fatal("loopback candidate should remain available")
	}
	movedRoute := snap
	movedRoute.routes = []defaultRoute{{IfIndex: 5, Family: "v4", Gateway: "198.51.100.1"}}
	if routeAvailableSnapshot(movedRoute, candidateIP) {
		t.Fatal("candidate should be unavailable once default route moves off its interface")
	}
	wrongFamily := snap
	wrongFamily.routes = []defaultRoute{{IfIndex: 4, Family: "v6", Gateway: "fe80::1"}}
	if routeAvailableSnapshot(wrongFamily, candidateIP) {
		t.Fatal("IPv4 candidate should be unavailable without an IPv4 default route on its interface")
	}
}

func TestFingerprintIgnoresIPv6PrivacyAddressChurnAndDetectsPrefixChange(t *testing.T) {
	en0 := net.Interface{Index: 4, Name: "en0", Flags: net.FlagUp | net.FlagMulticast}
	mkSnap := func(v6s ...string) networkSnapshot {
		list := []net.Addr{&net.IPNet{IP: net.ParseIP("192.0.2.10"), Mask: net.CIDRMask(24, 32)}}
		for _, s := range v6s {
			_, ipNet, err := net.ParseCIDR(s)
			if err != nil {
				t.Fatal(err)
			}
			// Preserve host bits as net.Interface.Addrs() does.
			ip, _, _ := net.ParseCIDR(s)
			list = append(list, &net.IPNet{IP: ip, Mask: ipNet.Mask})
		}
		return networkSnapshot{
			interfaces: []net.Interface{en0},
			addrs:      func(net.Interface) ([]net.Addr, error) { return list, nil },
			routes: []defaultRoute{
				{IfIndex: 4, Family: "v4", Gateway: "192.0.2.1"},
				{IfIndex: 4, Family: "v6", Gateway: "fe80::1"},
			},
			dnsLines: []string{"dns:nameserver 192.0.2.53"},
		}
	}
	a := fingerprintSnapshot(mkSnap("2001:db8:1:2::a1b2/64"))
	b := fingerprintSnapshot(mkSnap("2001:db8:1:2::c3d4/64"))
	c := fingerprintSnapshot(mkSnap("2001:db8:1:2::a1b2/64", "2001:db8:1:2::e5f6/64"))
	if a != b || a != c {
		t.Fatal("RFC 4941 IPv6 temporary address churn in same /64 changed network fingerprint")
	}
	otherPrefix := fingerprintSnapshot(mkSnap("2001:db8:9:9::a1b2/64"))
	if a == otherPrefix {
		t.Fatal("IPv6 /64 prefix change did not change network fingerprint")
	}
}

func TestObserveNetworkChangesDebouncesBurstHints(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		hints := make(chan struct{})
		ticks := make(chan time.Time)
		var scans atomic.Int32
		var snapshot atomic.Value
		snapshot.Store(sha256.Sum256([]byte("init")))
		out := observeNetworkChangesWithDebounce(ctx, hints, ticks, 60*time.Millisecond, func() [32]byte {
			scans.Add(1)
			return snapshot.Load().([32]byte)
		}, func() {})
		synctest.Wait()
		if scans.Load() != 1 {
			t.Fatalf("initial scans = %d, want 1", scans.Load())
		}
		// Three rapid hints (IPv4, IPv6, DNS) within 40ms must coalesce into one scan and one emission.
		snapshot.Store(sha256.Sum256([]byte("step1-ipv4")))
		hints <- struct{}{}
		time.Sleep(20 * time.Millisecond)
		snapshot.Store(sha256.Sum256([]byte("step2-ipv6")))
		hints <- struct{}{}
		time.Sleep(20 * time.Millisecond)
		snapshot.Store(sha256.Sum256([]byte("step3-dns")))
		hints <- struct{}{}
		synctest.Wait()
		select {
		case gen := <-out:
			t.Fatalf("emitted generation %d before debounce settled", gen)
		default:
		}
		time.Sleep(60 * time.Millisecond)
		synctest.Wait()
		select {
		case gen := <-out:
			if gen != 1 {
				t.Fatalf("gen = %d, want 1", gen)
			}
		default:
			t.Fatal("expected debounced generation emission")
		}
		if scans.Load() != 2 {
			t.Fatalf("scans = %d, want 2 (initial + 1 coalesced burst)", scans.Load())
		}
	})
}
