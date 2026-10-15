// Package updater installs compatible, published daemon builds. It runs in a
// separate CLI process, never in the daemon that it restarts.
package updater

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const releases = "https://github.com/Lewin671/remote-agent-releases/releases"
const maxArchive = 32 << 20
const maxBinary = 64 << 20

var semver = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Release struct {
	Version   string `json:"version"`
	Protocol  int    `json:"protocol"`
	State     int    `json:"state"`
	Automatic bool   `json:"automatic"`
	Archive   string `json:"archive"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Available bool   `json:"available"`
	Allowed   bool   `json:"allowed"`
	Reason    string `json:"reason,omitempty"`
}

func versionParts(v string) ([3]uint64, error) {
	var out [3]uint64
	if !semver.MatchString(v) {
		return out, fmt.Errorf("not a stable release version: %q", v)
	}
	for i, s := range strings.Split(v, ".") {
		n, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return out, err
		}
		out[i] = n
	}
	return out, nil
}

func newer(a, b string) (bool, error) {
	x, err := versionParts(a)
	if err != nil {
		return false, err
	}
	y, err := versionParts(b)
	if err != nil {
		return false, err
	}
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i], nil
		}
	}
	return false, nil
}

// Properties deliberately accepts only the generated key=value format. Unknown
// fields (including the app's escaped release notes) are not interpreted.
func properties(b []byte) (map[string]string, error) {
	m := map[string]string{}
	s := bufio.NewScanner(strings.NewReader(string(b)))
	s.Buffer(make([]byte, 4096), 256<<10)
	for s.Scan() {
		line := strings.TrimSuffix(s.Text(), "\r")
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || k == "" {
			return nil, errors.New("invalid release properties")
		}
		if _, ok := m[k]; ok {
			return nil, fmt.Errorf("duplicate release field %s", k)
		}
		m[k] = v
	}
	return m, s.Err()
}

func downloadClient() *http.Client {
	return &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || req.URL.Scheme != "https" {
			return errors.New("unsafe update redirect")
		}
		return nil
	}}
}

func (u *Updater) fetch(ctx context.Context, path string, out io.Writer, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.baseURL+path, nil)
	if err != nil {
		return err
	}
	res, err := u.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("release download returned HTTP %d", res.StatusCode)
	}
	if res.ContentLength > limit {
		return errors.New("release download exceeds size limit")
	}
	n, err := io.Copy(out, io.LimitReader(res.Body, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return errors.New("release download exceeds size limit")
	}
	return nil
}

func (u *Updater) manifest(ctx context.Context, path string) (map[string]string, error) {
	var b strings.Builder
	if err := u.fetch(ctx, path, &b, 256<<10); err != nil {
		return nil, err
	}
	return properties([]byte(b.String()))
}

func (u *Updater) Check(ctx context.Context, version string) (Release, error) {
	var r Release
	if u.platform != "darwin-arm64" && u.platform != "darwin-amd64" && u.platform != "linux-arm64" && u.platform != "linux-amd64" {
		return r, fmt.Errorf("no daemon release for %s", u.platform)
	}
	if version == "" {
		m, err := u.manifest(ctx, "/latest/download/latest.properties")
		if err != nil {
			return r, err
		}
		version = m["version"]
	}
	version = strings.TrimPrefix(version, "v")
	if _, err := versionParts(version); err != nil {
		return r, err
	}
	// Pin every subsequent download to the selected tag, including its manifest.
	m, err := u.manifest(ctx, "/download/v"+version+"/latest.properties")
	if err != nil {
		return r, err
	}
	if m["version"] != version {
		return r, errors.New("tag and release manifest disagree")
	}
	r.Version = version
	r.Available, err = newer(version, u.current.Version)
	if err != nil {
		return r, err
	}
	if !r.Available {
		r.Reason = "already current or selected version is older"
		return r, nil
	}
	if m["daemonUpdate"] != "1" {
		r.Reason = "release has no supported daemon update manifest"
		return r, nil
	}
	r.Protocol, err = strconv.Atoi(m["protocol"])
	if err != nil || r.Protocol < 1 {
		return r, errors.New("invalid release protocol")
	}
	r.State, err = strconv.Atoi(m["daemonState"])
	if err != nil || r.State < 1 {
		return r, errors.New("invalid release state generation")
	}
	if m["daemonAutomatic"] != "true" && m["daemonAutomatic"] != "false" {
		return r, errors.New("invalid automatic update policy")
	}
	r.Automatic = m["daemonAutomatic"] == "true"
	r.Archive = "remote-agent-v" + version + "-" + u.platform + ".tar.gz"
	prefix := "daemon." + u.platform + "."
	r.SHA256 = m[prefix+"sha256"]
	r.Size, err = strconv.ParseInt(m[prefix+"size"], 10, 64)
	if err != nil || r.Size <= 0 || r.Size > maxArchive || !digestPattern.MatchString(r.SHA256) {
		return r, errors.New("invalid daemon archive metadata")
	}
	switch {
	case u.current.Channel != "release":
		r.Reason = "self-update requires an official release installation"
	case r.Protocol != u.current.Protocol:
		r.Reason = "protocol changed; coordinate the upgrade with your clients"
	case r.State != u.current.State:
		r.Reason = "stored data changed; use the manual upgrade instructions"
	case !r.Automatic:
		r.Reason = "release requires a manual upgrade; read its release notes"
	default:
		r.Allowed = true
	}
	return r, nil
}

// stage accepts exactly the three regular files our daemon archive contains.
// Paths and links in downloaded archives never become filesystem destinations.
func (u *Updater) stage(ctx context.Context, r Release) (binary, notices string, err error) {
	a, err := os.CreateTemp(filepath.Dir(u.exe), ".remote-agent-download-*")
	if err != nil {
		return "", "", err
	}
	defer func() { a.Close(); os.Remove(a.Name()) }()
	h := sha256.New()
	if err = u.fetch(ctx, "/download/v"+r.Version+"/"+r.Archive, io.MultiWriter(a, h), r.Size); err != nil {
		return
	}
	pos, err := a.Seek(0, io.SeekCurrent)
	if err != nil {
		return "", "", err
	}
	if pos != r.Size || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		return "", "", errors.New("daemon archive checksum or size mismatch")
	}
	if _, err = a.Seek(0, io.SeekStart); err != nil {
		return
	}
	gz, err := gzip.NewReader(a)
	if err != nil {
		return "", "", err
	}
	defer gz.Close()
	tr := tar.NewReader(io.LimitReader(gz, 80<<20))
	root := strings.TrimSuffix(r.Archive, ".tar.gz")
	seen := map[string]bool{}
	defer func() {
		if err != nil && binary != "" {
			os.Remove(binary)
		}
	}()
	for count := 0; ; count++ {
		if count > 8 {
			return binary, "", errors.New("too many archive entries")
		}
		hdr, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return binary, "", e
		}
		if hdr.Typeflag == tar.TypeDir && (hdr.Name == root || hdr.Name == root+"/") {
			continue
		}
		name := strings.TrimPrefix(hdr.Name, root+"/")
		limit := int64(4 << 20)
		if name == "remote-agent" {
			limit = maxBinary
		}
		if (name != "remote-agent" && name != "remote-agent-service" && name != "THIRD_PARTY_NOTICES.txt") || hdr.Name != root+"/"+name || hdr.Typeflag != tar.TypeReg || seen[name] || hdr.Size <= 0 || hdr.Size > limit {
			return binary, "", errors.New("unexpected daemon archive member")
		}
		seen[name] = true
		if name == "remote-agent" {
			f, e := os.CreateTemp(filepath.Dir(u.exe), ".remote-agent-stage-*")
			if e != nil {
				return binary, "", e
			}
			binary = f.Name()
			_, e = io.Copy(f, tr)
			if e == nil {
				e = f.Chmod(0755)
			}
			if e == nil {
				e = f.Sync()
			}
			closeErr := f.Close()
			if e == nil {
				e = closeErr
			}
			if e != nil {
				return binary, "", e
			}
		} else if name == "THIRD_PARTY_NOTICES.txt" {
			b, e := io.ReadAll(tr)
			if e != nil {
				return binary, "", e
			}
			notices = string(b)
		}
	}
	if len(seen) != 3 {
		return binary, "", errors.New("incomplete daemon archive")
	}
	return binary, notices, nil
}
