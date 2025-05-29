package vnc

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"io"
)

// An Encoding implements a method for encoding pixel data that is
// sent by the server to the client.
type Encoding interface {
	// The number that uniquely identifies this encoding type.
	Type() int32

	// Read reads the contents of the encoded pixel data from the reader.
	// This should return a new Encoding implementation that contains
	// the proper data.
	Read(*ClientConn, *Rectangle, io.Reader) (Encoding, error)
}

// RawEncoding is raw pixel data sent by the server.
//
// See RFC 6143 Section 7.7.1
type RawEncoding struct {
	Colors []Color
}

func (*RawEncoding) Type() int32 {
	return 0
}

func (*RawEncoding) Read(c *ClientConn, rect *Rectangle, r io.Reader) (Encoding, error) {
	bytesPerPixel := c.PixelFormat.BPP / 8
	pixelBytes := make([]uint8, bytesPerPixel)

	var byteOrder binary.ByteOrder = binary.LittleEndian
	if c.PixelFormat.BigEndian {
		byteOrder = binary.BigEndian
	}

	colors := make([]Color, int(rect.Height)*int(rect.Width))

	for y := uint16(0); y < rect.Height; y++ {
		for x := uint16(0); x < rect.Width; x++ {
			if _, err := io.ReadFull(r, pixelBytes); err != nil {
				return nil, err
			}

			var rawPixel uint32
			if c.PixelFormat.BPP == 8 {
				rawPixel = uint32(pixelBytes[0])
			} else if c.PixelFormat.BPP == 16 {
				rawPixel = uint32(byteOrder.Uint16(pixelBytes))
			} else if c.PixelFormat.BPP == 32 {
				rawPixel = byteOrder.Uint32(pixelBytes)
			}

			color := &colors[int(y)*int(rect.Width)+int(x)]
			if c.PixelFormat.TrueColor {
				color.R = uint16((rawPixel >> c.PixelFormat.RedShift) & uint32(c.PixelFormat.RedMax))
				color.G = uint16((rawPixel >> c.PixelFormat.GreenShift) & uint32(c.PixelFormat.GreenMax))
				color.B = uint16((rawPixel >> c.PixelFormat.BlueShift) & uint32(c.PixelFormat.BlueMax))
			} else {
				*color = c.ColorMap[rawPixel]
			}
		}
	}

	return &RawEncoding{colors}, nil
}

// DesktopSize Pseudo-Encoding declares that the client is capable
// of coping with a change in the framebuffer width and height.
//
// See RFC 6143 7.8.2
type DesktopSizePseudoEncoding struct{}

func (*DesktopSizePseudoEncoding) Read(c *vnc.ClientConn, rect *vnc.Rectangle, r io.Reader) (vnc.Encoding, error) {
	c.FrameBufferWidth = rect.Width
	c.FrameBufferHeight = rect.Height
	return &DesktopSizePseudoEncoding{}, nil
}

func (*DesktopSizePseudoEncoding) Type() int32 {
	return -223
}

// ZlibEncoding is Zlib encoded pixel data
//
// See RFC 6143 8.4.2
type ZlibEncoding struct {
	Colors     []vnc.Color
	zlibReader *io.ReadCloser
	zlibData   bytes.Buffer
}

func (ze *ZlibEncoding) Read(c *vnc.ClientConn, rect *vnc.Rectangle, r io.Reader) (vnc.Encoding, error) {
	var compressedLength uint32
	if err := binary.Read(r, binary.BigEndian, &compressedLength); err != nil {
		return nil, err
	}

	// The RFB protocol expects us to read the entire compressed length;
	// no more (which could happen if we just passed the reader through
	// zlib.NewReader, due to the input not being a io.ByteReader), and
	// no less (which could happen if the compressed length was larger
	// than what's strictly required for the rect's colors), so we read
	// all of the data up front, appending it to a buffer that the zlib
	// decoding processes independently.
	limitedReader := io.LimitedReader{r, int64(compressedLength)}
	readBytes, err := io.Copy(&ze.zlibData, &limitedReader)
	if uint32(readBytes) != compressedLength || err != nil {
		return nil, err
	}

	// A single zlib stream is used for each RFB protocol connection,
	// so we must re-use the zlib reader between each decode, as we
	// can only read the zlib header once.
	if ze.zlibReader == nil {
		if zlibReader, err := zlib.NewReader(&ze.zlibData); err != nil {
			return nil, err
		} else {
			ze.zlibReader = &zlibReader
		}
	}

	if rawEnc, err := (&vnc.RawEncoding{}).Read(c, rect, *ze.zlibReader); err != nil {
		return nil, err
	} else {
		return &ZlibEncoding{Colors: rawEnc.(*vnc.RawEncoding).Colors}, nil
	}
}

func (*ZlibEncoding) Type() int32 {
	return 6
}
