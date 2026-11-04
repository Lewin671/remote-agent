package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/png"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
)

func uploadRedImage(ctx context.Context, client *remoteclient.Client, machine string) (string, error) {
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		return "", err
	}
	sum := sha256.Sum256(data.Bytes())
	id := remoteclient.NewRequestID()
	result, err := client.DoMachine(ctx, machine, &pb.Command{Kind: &pb.Command_BeginImageUpload{BeginImageUpload: &pb.BeginImageUploadRequest{TransferId: id, Format: pb.ImageFormat_IMAGE_FORMAT_PNG, TotalBytes: int64(data.Len()), Sha256: sum[:]}}})
	if err != nil {
		return "", err
	}
	chunk := int(result.GetBeginImageUpload().GetStatus().GetMaxChunkBytes())
	if chunk <= 0 {
		return "", fmt.Errorf("invalid image chunk size")
	}
	for off := 0; off < data.Len(); off += chunk {
		_, err = client.DoMachine(ctx, machine, &pb.Command{Kind: &pb.Command_PutImageChunk{PutImageChunk: &pb.PutImageChunkRequest{TransferId: id, Offset: int64(off), Data: data.Bytes()[off:min(data.Len(), off+chunk)]}}})
		if err != nil {
			return "", err
		}
	}
	result, err = client.DoMachine(ctx, machine, &pb.Command{Kind: &pb.Command_CompleteImageUpload{CompleteImageUpload: &pb.CompleteImageUploadRequest{TransferId: id}}})
	if err != nil {
		return "", err
	}
	imageID := result.GetCompleteImageUpload().GetStatus().GetImageId()
	if imageID == "" {
		return "", fmt.Errorf("image upload did not complete")
	}
	return imageID, nil
}
