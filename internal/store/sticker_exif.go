package store

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
)

// stickerExifPayload adalah struktur JSON yang WhatsApp baca dari chunk EXIF
// webp untuk menentukan nama pack & publisher (author) sebuah stiker.
type stickerExifPayload struct {
	PackID    string   `json:"sticker-pack-id"`
	PackName  string   `json:"sticker-pack-name"`
	Publisher string   `json:"sticker-pack-publisher"`
	Emojis    []string `json:"emojis"`
}

// exifHeader adalah 24 byte tetap yang WhatsApp expect sebelum payload JSON
// di dalam chunk EXIF webp. Ini konstanta yang dipakai luas oleh berbagai
// library sticker WhatsApp (mis. wa-sticker-formatter, node sticker libs).
var exifHeader = []byte{
	0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00,
	0x01, 0x00, 0x41, 0x57, 0x07, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x16, 0x00, 0x00, 0x00,
}

// buildStickerExif membangun full byte chunk EXIF (header + panjang json + json)
// yang siap dimasukkan sebagai chunk RIFF "EXIF" ke dalam file webp.
func buildStickerExif(packname, author string) ([]byte, error) {
	payload := stickerExifPayload{
		PackID:    "wabot-go",
		PackName:  packname,
		Publisher: author,
		Emojis:    []string{"😀"},
	}
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("gagal marshal exif json: %w", err)
	}

	buf := make([]byte, len(exifHeader))
	copy(buf, exifHeader)
	// 4 byte terakhir dari header (offset 16) adalah panjang payload JSON,
	// little-endian uint32 — perlu diisi ulang sesuai panjang json aktual.
	binary.LittleEndian.PutUint32(buf[14:18], uint32(len(jsonBytes)))

	return append(buf, jsonBytes...), nil
}

// riffChunk merepresentasikan satu chunk RIFF (fourCC + data).
type riffChunk struct {
	fourCC string
	data   []byte
}

// parseWebpRiff membongkar file webp jadi list chunk RIFF-nya, tanpa
// termasuk header RIFF utama (12 byte pertama: "RIFF"+size+"WEBP").
func parseWebpRiff(webp []byte) (chunks []riffChunk, err error) {
	if len(webp) < 12 || string(webp[0:4]) != "RIFF" || string(webp[8:12]) != "WEBP" {
		return nil, fmt.Errorf("bukan file webp yang valid")
	}

	pos := 12
	for pos+8 <= len(webp) {
		fourCC := string(webp[pos : pos+4])
		size := binary.LittleEndian.Uint32(webp[pos+4 : pos+8])
		dataStart := pos + 8
		dataEnd := dataStart + int(size)
		if dataEnd > len(webp) {
			return nil, fmt.Errorf("chunk %q rusak: melebihi panjang file", fourCC)
		}
		chunks = append(chunks, riffChunk{fourCC: fourCC, data: webp[dataStart:dataEnd]})

		// Chunk RIFF di-pad ke jumlah byte genap.
		pos = dataEnd
		if size%2 == 1 {
			pos++
		}
	}
	return chunks, nil
}

// putUint24LE menulis 3 byte little-endian ke dst (dipakai untuk field
// width-1/height-1 di chunk VP8X yang memang cuma 24-bit, bukan 32-bit).
func putUint24LE(dst []byte, v uint32) {
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
	dst[2] = byte(v >> 16)
}

// webpDimensions mencari lebar & tinggi gambar dari chunk VP8X (kalau ada),
// atau dari chunk VP8 /VP8L/ANMF sebagai fallback untuk webp simpel/animasi
// yang belum extended format.
func webpDimensions(chunks []riffChunk) (width, height int, err error) {
	for _, c := range chunks {
		switch c.fourCC {
		case "VP8X":
			if len(c.data) >= 10 {
				w := int(c.data[4]) | int(c.data[5])<<8 | int(c.data[6])<<16
				h := int(c.data[7]) | int(c.data[8])<<8 | int(c.data[9])<<16
				return w + 1, h + 1, nil
			}
		case "VP8 ":
			// Bitstream VP8 lossy: dimensi ada di offset 6-9 dari data
			// (setelah 3-byte start code + tag), masing-masing 14-bit.
			if len(c.data) >= 10 {
				w := int(binary.LittleEndian.Uint16(c.data[6:8])) & 0x3FFF
				h := int(binary.LittleEndian.Uint16(c.data[8:10])) & 0x3FFF
				return w, h, nil
			}
		case "VP8L":
			// Bitstream VP8L lossless: signature byte lalu 14-bit width-1
			// dan 14-bit height-1 dikemas little-endian mulai offset 1.
			if len(c.data) >= 5 {
				bits := uint32(c.data[1]) | uint32(c.data[2])<<8 | uint32(c.data[3])<<16 | uint32(c.data[4])<<24
				w := int(bits&0x3FFF) + 1
				h := int((bits>>14)&0x3FFF) + 1
				return w, h, nil
			}
		case "ANMF":
			// Frame animasi: width-1/height-1 masing-masing 24-bit di
			// offset 6 dan 9 dari data frame header.
			if len(c.data) >= 12 {
				w := int(c.data[6]) | int(c.data[7])<<8 | int(c.data[8])<<16
				h := int(c.data[9]) | int(c.data[10])<<8 | int(c.data[11])<<16
				return w + 1, h + 1, nil
			}
		}
	}
	return 0, 0, fmt.Errorf("tidak bisa membaca dimensi webp: chunk VP8X/VP8/VP8L/ANMF tidak ditemukan")
}

// ReadWebpDimensions membaca lebar & tinggi sebuah file webp. Dipakai oleh
// plugin (mis. sticker.go) untuk mengisi field Width/Height StickerMessage
// secara akurat, karena sejak convertToStickerWebp tidak lagi memaksa
// pad ke 512x512, dimensi keluaran bisa bervariasi sesuai rasio aspek asli.
func ReadWebpDimensions(webp []byte) (width, height int, err error) {
	chunks, err := parseWebpRiff(webp)
	if err != nil {
		return 0, 0, err
	}
	return webpDimensions(chunks)
}

// WriteStickerExif menyisipkan/mengganti chunk EXIF di sebuah file webp dengan
// metadata packname & author baru, lalu mengembalikan webp yang sudah diedit.
// Chunk EXIF lama (kalau ada) dibuang dan diganti yang baru — chunk lain
// (VP8/VP8L/VP8X/ANIM/ANMF/ALPH, dst) dipertahankan apa adanya.
func WriteStickerExif(webp []byte, packname, author string) ([]byte, error) {
	chunks, err := parseWebpRiff(webp)
	if err != nil {
		return nil, err
	}

	exifData, err := buildStickerExif(packname, author)
	if err != nil {
		return nil, err
	}

	width, height, err := webpDimensions(chunks)
	if err != nil {
		return nil, err
	}

	hasVP8X := false
	var newChunks []riffChunk
	for _, c := range chunks {
		if c.fourCC == "EXIF" {
			continue // buang chunk EXIF lama
		}
		if c.fourCC == "VP8X" {
			hasVP8X = true
			// Set flag bit EXIF (bit 3 dari byte flag pertama) supaya
			// reader tahu file ini punya metadata EXIF.
			flagged := make([]byte, len(c.data))
			copy(flagged, c.data)
			if len(flagged) > 0 {
				flagged[0] |= 0x08
			}
			c.data = flagged
		}
		newChunks = append(newChunks, c)
	}

	if !hasVP8X {
		// Output ffmpeg untuk gambar statis simpel sering berupa "VP8 "/"VP8L"
		// polos tanpa VP8X (extended format). Bangun VP8X sendiri di depan
		// supaya file tetap valid dan bisa disisipi EXIF.
		vp8x := make([]byte, 10)
		vp8x[0] = 0x08 // flag: bit EXIF (bit 3) nyala, sisanya 0
		putUint24LE(vp8x[4:7], uint32(width-1))
		putUint24LE(vp8x[7:10], uint32(height-1))
		newChunks = append([]riffChunk{{fourCC: "VP8X", data: vp8x}}, newChunks...)
	}

	// Chunk EXIF ditaruh setelah chunk gambar/animasi utama (di akhir),
	// sesuai konvensi umum penulisan webp extended.
	newChunks = append(newChunks, riffChunk{fourCC: "EXIF", data: exifData})

	var body bytes.Buffer
	for _, c := range newChunks {
		body.WriteString(c.fourCC)
		sizeBuf := make([]byte, 4)
		binary.LittleEndian.PutUint32(sizeBuf, uint32(len(c.data)))
		body.Write(sizeBuf)
		body.Write(c.data)
		if len(c.data)%2 == 1 {
			body.WriteByte(0) // padding byte
		}
	}

	var out bytes.Buffer
	out.WriteString("RIFF")
	sizeBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(sizeBuf, uint32(4+body.Len())) // "WEBP" + body
	out.Write(sizeBuf)
	out.WriteString("WEBP")
	out.Write(body.Bytes())

	return out.Bytes(), nil
}
