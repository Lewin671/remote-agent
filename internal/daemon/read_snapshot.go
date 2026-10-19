package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/contextio"
	"github.com/Lewin671/remote-agent/internal/imageproc"
	"github.com/Lewin671/remote-agent/internal/paths"
)

// Read-only RPCs read atomic file snapshots. They do not run GC, mutate image
// indexes, or leave an uncancellable filesystem worker owning a daemon mutex.
func (u *uploads) status(ctx context.Context, id, device string) (*pb.ImageUploadStatus, error) {
	if !validHexID(id) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a transfer ID is 32 hex digits"))
	}
	b, err := contextio.ReadFile(ctx, u.partial(id)+".json")
	if os.IsNotExist(err) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such transfer; it may have expired"))
	}
	if err != nil {
		return nil, err
	}
	t := &transfer{}
	if err := json.Unmarshal(b, t); err != nil {
		return nil, err
	}
	if t.Session == "" && time.Now().UnixMilli() >= t.expiresMs() {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("the transfer expired; upload it again"))
	}
	if err := u.owned(t, device); err != nil {
		return nil, err
	}
	return t.status(), nil
}

func (d *Daemon) imageStorageSnapshot(ctx context.Context) (*pb.ImageStorageUsage, error) {
	// Capture in-memory protection facts before entering the filesystem worker.
	pins := d.protectedImageSessions()
	uploadsDir := d.uploads.dir
	sessionsDir := paths.SessionsDir()
	return contextio.Read(ctx, func() (*pb.ImageStorageUsage, error) {
		usage := &pb.ImageStorageUsage{}
		sessions := map[string]*pb.ImageSessionStorage{}
		known := map[string]bool{}
		row := func(id string) *pb.ImageSessionStorage {
			if sessions[id] == nil {
				sessions[id] = &pb.ImageSessionStorage{SessionId: id}
			}
			return sessions[id]
		}
		records, err := os.ReadDir(filepath.Join(uploadsDir, "partial"))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, e := range records {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(uploadsDir, "partial", e.Name()))
			if err != nil {
				continue
			}
			t := &transfer{}
			if json.Unmarshal(b, t) != nil {
				continue
			}
			if t.Session == "" && time.Now().UnixMilli() >= t.expiresMs() {
				continue
			}
			if t.ImageID == "" {
				usage.ReservedBytes += max(int64(0), t.Total-t.Persisted)
				continue
			}
			owner := "pending"
			if t.Session != "" {
				owner = t.Session
			}
			path := filepath.Join(uploadsDir, owner, t.ImageID+"."+imageproc.Ext(t.Format))
			fi, err := os.Stat(path)
			if err != nil {
				continue
			}
			known[path] = true
			if t.Session != "" {
				row(t.Session).Bytes += fi.Size()
			}
			if reclaimable(t, pins) {
				usage.ReclaimableBytes += fi.Size()
			} else {
				usage.ProtectedBytes += fi.Size()
				if t.Session != "" {
					row(t.Session).ProtectedBytes += fi.Size()
				}
			}
		}
		err = filepath.WalkDir(uploadsDir, func(path string, e fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if !e.Type().IsRegular() {
				return nil
			}
			fi, err := e.Info()
			if err != nil {
				return err
			}
			usage.UploadBytes += fi.Size()
			ext := filepath.Ext(path)
			if !known[path] && (ext == ".part" || ext == ".png" || ext == ".jpg" || ext == ".webp" || ext == ".gif") {
				usage.ProtectedBytes += fi.Size()
				rel, _ := filepath.Rel(uploadsDir, path)
				owner := strings.Split(rel, string(filepath.Separator))[0]
				if owner != "partial" && owner != "pending" {
					row(owner).Bytes += fi.Size()
					row(owner).ProtectedBytes += fi.Size()
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(sessionsDir)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !entry.IsDir() {
				continue
			}
			files, err := os.ReadDir(filepath.Join(sessionsDir, entry.Name(), "images"))
			if err != nil && !os.IsNotExist(err) {
				return nil, err
			}
			for _, file := range files {
				if strings.HasSuffix(file.Name(), ".display") || strings.HasSuffix(file.Name(), ".preview") {
					fi, err := file.Info()
					if err != nil {
						return nil, err
					}
					usage.CacheBytes += fi.Size()
				}
			}
		}
		for _, r := range sessions {
			usage.Sessions = append(usage.Sessions, r)
		}
		sort.Slice(usage.Sessions, func(i, j int) bool { return usage.Sessions[i].Bytes > usage.Sessions[j].Bytes })
		return usage, nil
	})
}
