package remoteclient

import (
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"time"
)

// Reseal authorizes an existing request with the current content key. The
// original unpacked bytes, scope, ID and deadline are retained verbatim.
// Ordinary retries should call Forward with their existing envelope.
func (c *Client) Reseal(envelope *pb.Envelope) (*pb.Envelope, error) {
	wire, err := deviceauth.FromEnvelope(envelope)
	if err != nil {
		return nil, err
	}
	header, packed, err := c.endpoint.Open(wire, nil, false, uint64(time.Now().UnixMilli()))
	if err != nil {
		return nil, err
	}
	if header.Sender != c.DeviceID || header.Purpose != deviceauth.Command {
		return nil, deviceauth.ErrInvalid
	}
	plain, err := deviceauth.Unpack(header, packed)
	if err != nil {
		return nil, err
	}
	header.Compression = 0
	sealed, err := c.endpoint.Prepare(header, nil, plain)
	if err != nil {
		return nil, err
	}
	return sealed.Envelope()
}
