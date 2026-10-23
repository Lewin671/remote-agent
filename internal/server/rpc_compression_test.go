package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestResponseCompressionByRPCAndCodec(t *testing.T) {
	ts := newTestServer(t)
	a := ts.account(t, ts.keys)
	a.putSession(t, "compression")
	ctx := context.Background()
	const imageID = "00000000000000000000000000000001"
	env := &pb.Envelope{Ciphertext: bytes.Repeat([]byte("sealed"), 1000)}
	raw, err := proto.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.tenant().putImage(ctx, a.mid, "compression", imageID, raw, time.Hour, 1<<20, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ts.tenant().appendEvents(ctx, a.mid, "compression", []*pb.StoredEvent{{Seq: 1, Envelope: env}}, nil); err != nil {
		t.Fatal(err)
	}
	handler := ts.srv.Handler()
	for _, transport := range []string{"connect", "grpc", "grpc-web"} {
		for _, codec := range []string{"proto", "json"} {
			for _, rpc := range []string{"GetImage", "GetEvents"} {
				for _, inbound := range []bool{false, true} {
					for _, accept := range []string{"identity", "gzip"} {
						name := transport + "/" + codec + "/" + rpc + "/" + accept
						if inbound {
							name += "/gzip-request"
						}
						t.Run(name, func(t *testing.T) {
							var msg, result proto.Message
							if rpc == "GetImage" {
								msg = &pb.GetImageRequest{SessionId: "compression", ImageId: imageID}
								result = &pb.GetImageResponse{}
							} else {
								msg = &pb.GetEventsRequest{SessionId: "compression"}
								result = &pb.GetEventsResponse{}
							}
							var body []byte
							var err error
							if codec == "proto" {
								body, err = proto.Marshal(msg)
							} else {
								body, err = protojson.Marshal(msg)
							}
							if err != nil {
								t.Fatal(err)
							}
							if inbound {
								body = gzipTestBody(t, body)
							}
							contentType := "application/" + codec
							acceptHeader, encodingHeader := "Accept-Encoding", "Content-Encoding"
							if transport != "connect" {
								contentType = "application/" + transport + "+" + codec
								acceptHeader, encodingHeader = "Grpc-Accept-Encoding", "Grpc-Encoding"
								frame := make([]byte, 5, len(body)+5)
								if inbound {
									frame[0] = 1
								}
								binary.BigEndian.PutUint32(frame[1:], uint32(len(body)))
								body = append(frame, body...)
							}
							req := httptest.NewRequest(http.MethodPost, "/"+remoteagentv1connect.ClientServiceName+"/"+rpc, bytes.NewReader(body))
							req.Header.Set("Content-Type", contentType)
							req.Header.Set("Authorization", "Bearer "+a.ctok)
							req.Header.Set(acceptHeader, accept)
							req.Header.Set("Connect-Protocol-Version", "1")
							if inbound {
								req.Header.Set(encodingHeader, "gzip")
							}
							rec := httptest.NewRecorder()
							handler.ServeHTTP(rec, req)
							if rec.Code != http.StatusOK {
								t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
							}
							wantGzip := inbound || accept == "gzip"
							if transport == "connect" && codec == "proto" && rpc == "GetImage" {
								wantGzip = false
							}
							gotGzip := rec.Header().Get(encodingHeader) == "gzip"
							if gotGzip != wantGzip {
								t.Fatalf("response encoding = %q, want gzip=%v", rec.Header().Get(encodingHeader), wantGzip)
							}
							response := rec.Body.Bytes()
							if transport != "connect" {
								if len(response) < 5 {
									t.Fatalf("short frame: %x", response)
								}
								n := int(binary.BigEndian.Uint32(response[1:5]))
								gotGzip = response[0]&1 != 0
								response = response[5 : 5+n]
							}
							if gotGzip {
								gz, err := gzip.NewReader(bytes.NewReader(response))
								if err != nil {
									t.Fatal(err)
								}
								response, err = io.ReadAll(gz)
								if err != nil {
									t.Fatal(err)
								}
								gz.Close()
							}
							if codec == "proto" {
								err = proto.Unmarshal(response, result)
							} else {
								err = protojson.Unmarshal(response, result)
							}
							if err != nil {
								t.Fatal(err)
							}
							if rpc == "GetImage" {
								if !proto.Equal(result.(*pb.GetImageResponse).Image, env) {
									t.Fatal("image changed")
								}
							} else if got := result.(*pb.GetEventsResponse).Events; len(got) != 1 || !proto.Equal(got[0].Envelope, env) {
								t.Fatal("events changed")
							}
						})
					}
				}
			}
		}
	}
}

func gzipTestBody(t *testing.T, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
