package plugins

// Token DRM Edge Read Aloud ("Sec-MS-GEC") versi 2.
//
// Sejak Oktober 2026 server Edge Read Aloud menolak token v1 (SHA256 heks
// biasa): handshake WebSocket dijawab 403 "Invalid response status". Token
// v2 berbentuk "<16 hex CS64>-<64 hex SHA256>" dan nilai yang dikirim di
// query Sec-MS-GEC-Version harus berawalan "2-" (lihat ttsGECVersion di
// tts.go). Kombinasi salah satu saja = 403, sudah diuji langsung.
//
// Implementasi ini port dari drm.py edge-tts 7.3.1 (generate_sec_ms_gec_v2).
// Semua aritmetika dibatasi 32-bit: operasi dilakukan di uint64 lalu di-mask,
// karena Python memakai integer tak terbatas (rotasi/shift boleh "meluber"
// dulu sebelum dijumlahkan). Vektor ujinya ada di tts_gec_test.go dan
// dihasilkan langsung oleh edge-tts 7.3.1.

import (
	"encoding/binary"
	"encoding/hex"
	"math"
	"strconv"
)

const (
	// ttsTrustedClientToken token publik klien Edge (nilai sama dengan
	// edge-tts). Dipakai di URL WebSocket dan sebagai garam token.
	ttsTrustedClientToken = "6A5AA1D4EAFF4E9FB37E23D68491D6F4"

	// ttsWinEpoch selisih detik epoch file-Windows (1601) ke Unix (1970).
	ttsWinEpoch = 11644473600

	// Penyesuaian tetap milik skema token v2 (jangan diubah tanpa menyalin
	// ulang dari drm.py: nilainya membuat hash "mirip SHA256" ini berbeda
	// dari SHA256 standar).
	sha256Adjustment       uint32 = 0xC9DC06A7
	sha256Sigma0Adjustment uint32 = 0x059C0B80
	sha256Sigma1Adjustment uint32 = 0x000B92C0
	cs64Factor             uint32 = 0x82893933
	cs64Adjustment0        uint32 = 0xF2F90B1A
	cs64Adjustment1        uint32 = 0x0B1A0000
)

var ttsSHA256Initial = [8]uint32{
	0x6A09E667, 0xBB67AE85, 0x3C6EF372, 0xA54FF53A,
	0x510E527F, 0x9B05688C, 0x1F83D9AB, 0x5BE0CD19,
}

var ttsSHA256State = [8]uint32{
	0xFD9FBEA2, 0x91243CE7, 0xBE499094, 0xCD2E1E0A,
	0x6C006F49, 0x04BD35AB, 0xFEB0A7E4, 0x03EFFBD2,
}

var ttsSHA256Schedule = [14]uint32{
	0xEB24BF9E, 0x1558691F, 0xA0C09EE2, 0x0CFA32BA,
	0xA622EE8C, 0xD1DD9491, 0xC0DD25A0, 0xF9E70AB6,
	0xF0C31573, 0x7058CE4E, 0x7188E606, 0x00E5AB33,
	0xD258B060, 0x72766564,
}

var ttsSHA256RoundConstants = [64]uint32{
	0x428A2F98, 0x71374491, 0xB5C0FBCF, 0xE9B5DBA5, 0x3956C25B, 0x59F111F1, 0x923F82A4, 0xAB1C5ED5,
	0xD807AA98, 0x12835B01, 0x243185BE, 0x550C7DC3, 0x72BE5D74, 0x80DEB1FE, 0x9BDC06A7, 0xC19BF174,
	0xE49B69C1, 0xEFBE4786, 0x0FC19DC6, 0x240CA1CC, 0x2DE92C6F, 0x4A7484AA, 0x5CB0A9DC, 0x76F988DA,
	0x983E5152, 0xA831C66D, 0xB00327C8, 0xBF597FC7, 0xC6E00BF3, 0xD5A79147, 0x06CA6351, 0x14292967,
	0x27B70A85, 0x2E1B2138, 0x4D2C6DFC, 0x53380D13, 0x650A7354, 0x766A0ABB, 0x81C2C92E, 0x92722C85,
	0xA2BFE8A1, 0xA81A664B, 0xC24B8B70, 0xC76C51A3, 0xD192E819, 0xD6990624, 0xF40E3585, 0x106AA070,
	0x19A4C116, 0x1E376C08, 0x2748774C, 0x34B0BCB5, 0x391C0CB3, 0x4ED8AA4A, 0x5B9CCA4F, 0x682E6FF3,
	0x748F82EE, 0x78A5636F, 0x84C87814, 0x8CC70208, 0x90BEFFFA, 0xA4506CEB, 0xBEF9A3F7, 0xC67178F2,
}

// ttsCS64Coefficients pasangan (koefisien, offset) untuk digest CS64.
var ttsCS64Coefficients = [14][2]uint32{
	{0xE82E91B1, 0xB32700D0},
	{0x44DE24C6, 0x88DFF51D},
	{0x3BEB65BC, 0x29514038},
	{0xC89C5933, 0xB328CBDF},
	{0x8D1EC211, 0xD405B4D5},
	{0x314EF5D5, 0x26E55F3C},
	{0xE92491B1, 0x607801C6},
	{0xF0A5B9D5, 0x82B2C1FB},
	{0x06064CF3, 0xFA51A3DA},
	{0xDA79FA60, 0x207CC464},
	{0xBED20560, 0x3C3C74F3},
	{0x89F9C9F8, 0xD2D14B61},
	{0x35FA38C6, 0xBB04A2C1},
	{0xCB49E3C6, 0xC5626167},
}

// ttsU32 memotong nilai 64-bit ke 32-bit rendah.
func ttsU32(v uint64) uint32 { return uint32(v & 0xFFFFFFFF) }

// ttsRol16 rotasi 16-bit (untuk 32-bit rotasi kiri 16 == rotasi kanan 16).
func ttsRol16(v uint32) uint32 { return v<<16 | v>>16 }

// ttsSigma0 / ttsSigma1 varian shift yang dipakai saat memperluas jadwal.
func ttsSigma0(v uint32) uint32 {
	x := uint64(v)
	return ttsU32(x>>7 ^ x<<25 ^ x>>18 ^ x<<14 ^ x>>3)
}

func ttsSigma1(v uint32) uint32 {
	x := uint64(v)
	return ttsU32(x>>17 ^ x<<15 ^ x>>19 ^ x<<13 ^ x>>10)
}

// ttsSchedSigma0 / ttsSchedSigma1 versi tanpa mask (dijumlahkan dulu oleh
// pemanggil, baru dipotong) — persis seperti drm.py.
func ttsSchedSigma0(v uint32) uint64 {
	x := uint64(v)
	return x>>7 ^ x<<25 ^ x>>18 ^ x<<14 ^ x>>3
}

func ttsSchedSigma1(v uint32) uint64 {
	x := uint64(v)
	return x>>17 ^ x<<15 ^ x>>19 ^ x<<13 ^ x>>10
}

// ttsBigSigmaA / ttsBigSigmaE memakai trik "nilai * 0x100000001" milik
// drm.py: hasilnya ekuivalen dengan XOR tiga rotasi kanan.
func ttsBigSigmaE(e uint32) uint32 {
	rot := uint64(e) * 0x100000001
	return ttsU32(rot>>6 ^ rot>>11 ^ rot>>25)
}

func ttsBigSigmaA(a uint32) uint32 {
	rot := uint64(a) * 0x100000001
	return ttsU32(rot>>2 ^ rot>>13 ^ rot>>22)
}

// ttsExpandSchedule memperluas jadwal pesan sampai 64 word.
func ttsExpandSchedule(words []uint32, firstWord int) []uint32 {
	for index := firstWord; index < 64; index++ {
		x := words[index-2]
		y := words[index-15]
		sum := ttsSchedSigma1(x) + uint64(words[index-7]) + ttsSchedSigma0(y) + uint64(words[index-16])
		words = append(words, ttsU32(sum))
	}
	return words
}

// ttsSHA256Rounds menjalankan ronde kompresi. firstRound 14 dipakai untuk
// state awal token (14 ronde pertama sudah "dibakar" di tabel state).
func ttsSHA256Rounds(state [8]uint32, words []uint32, firstRound int) [8]uint32 {
	constants := ttsSHA256RoundConstants
	if firstRound == 14 {
		// Ronde ke-14 memakai konstanta yang sudah disesuaikan skema v2.
		constants[14] = sha256Adjustment
	}
	a, b, c, d, e, f, g, h := state[0], state[1], state[2], state[3], state[4], state[5], state[6], state[7]
	for index := firstRound; index < 64; index++ {
		t1 := uint64(h) + uint64(ttsBigSigmaE(e)) + uint64(g^(e&(f^g))) + uint64(constants[index]) + uint64(words[index])
		t2 := uint64(ttsBigSigmaA(a)) + uint64((a&b)|(c&(a|b)))
		a, b, c, d, e, f, g, h = ttsU32(t1+t2), a, b, c, ttsU32(uint64(d)+t1), e, f, g
	}
	return [8]uint32{a, b, c, d, e, f, g, h}
}

// ttsSHA256Init menyusun state awal dari 7 byte pertama garam (sisanya masuk
// blok pesan biasa).
func ttsSHA256Init(data7 []byte) [8]uint32 {
	words := make([]uint32, 14, 64)
	words = append(words,
		uint32(data7[0])<<16|uint32(data7[1])<<8|uint32(data7[2]),
		binary.BigEndian.Uint32(data7[3:7]),
	)
	for index := 16; index < 30; index++ {
		value := uint64(ttsSHA256Schedule[index-16])
		if index-2 >= 14 {
			s1 := uint64(ttsSigma1(words[index-2]))
			if index == 16 {
				s1 ^= uint64(sha256Sigma1Adjustment)
			}
			value += s1
		}
		if index-7 >= 14 {
			value += uint64(words[index-7])
		}
		if index-15 >= 14 {
			s0 := uint64(ttsSigma0(words[index-15]))
			if index == 29 {
				s0 ^= uint64(sha256Sigma0Adjustment)
			}
			value += s0
		}
		words = append(words, ttsU32(value))
	}
	sum := int64(ttsSigma1(words[28])) + int64(words[23]) + int64(ttsSigma0(words[15])) +
		int64(words[14]) + int64(sha256Adjustment) - int64(ttsSHA256RoundConstants[14])
	words = append(words, uint32(sum))
	words = ttsExpandSchedule(words, 31)
	working := ttsSHA256Rounds(ttsSHA256State, words, 14)
	var out [8]uint32
	for i := range out {
		out[i] = ttsSHA256Initial[i] + working[i]
	}
	return out
}

// ttsTokenSHA256 menghitung digest 32 byte dengan varian SHA256 milik token
// v2 (padding dan state awal tidak sama dengan SHA256 standar).
func ttsTokenSHA256(data []byte) []byte {
	length := 57 + len(data)
	pad := ((55 - length) % 64) + 64
	pad %= 64
	padded := append([]byte{}, data[7:]...)
	padded = append(padded, 0x80)
	padded = append(padded, make([]byte, pad)...)
	var lengthBytes [8]byte
	binary.BigEndian.PutUint64(lengthBytes[:], uint64(length)*8)
	padded = append(padded, lengthBytes[:]...)

	state := ttsSHA256Init(data[:7])
	for offset := 0; offset < len(padded); offset += 64 {
		words := make([]uint32, 16, 64)
		for i := 0; i < 16; i++ {
			words[i] = binary.BigEndian.Uint32(padded[offset+4*i : offset+4*i+4])
		}
		words = ttsExpandSchedule(words, 16)
		working := ttsSHA256Rounds(state, words, 0)
		for i := range state {
			state[i] += working[i]
		}
	}
	out := make([]byte, 32)
	for i := range state {
		binary.BigEndian.PutUint32(out[4*i:], state[i])
	}
	return out
}

// ttsWordsLE4Even memecah data jadi word little-endian 4 byte, panjang
// dibulatkan ke kelipatan 4 dan jumlah word dibuat genap.
func ttsWordsLE4Even(data []byte) []uint32 {
	padded := append([]byte{}, data...)
	if rem := len(padded) % 4; rem != 0 {
		padded = append(padded, make([]byte, 4-rem)...)
	}
	if len(padded) == 0 {
		return nil
	}
	words := make([]uint32, 0, len(padded)/4+1)
	for i := 0; i < len(padded); i += 4 {
		words = append(words, binary.LittleEndian.Uint32(padded[i:i+4]))
	}
	if len(words)%2 != 0 {
		words = append(words, 0)
	}
	return words
}

// ttsCS64Digest menghitung dua word digest CS64 (dicampur dengan SHA256 dari
// data yang sama).
func ttsCS64Digest(data, digest []byte) (uint32, uint32) {
	keyA := binary.LittleEndian.Uint32(digest[0:4]) | 1
	keyB := binary.LittleEndian.Uint32(digest[4:8]) | 1
	factors := [2]uint32{keyA * cs64Factor, keyB * cs64Factor}

	terms := make([]uint32, 0, 64)
	offsets := make([]uint32, 0, 64)
	for i, coeff := range ttsCS64Coefficients {
		terms = append(terms, coeff[0]*factors[i%2])
		offsets = append(offsets, coeff[1])
	}
	for index, word := range ttsWordsLE4Even(append([]byte{0}, data...)) {
		key := keyA
		mul := uint32(0xEF0569FB)
		if index%2 != 0 {
			key = keyB
			mul = 0xC31713DB
		}
		term := uint64(word) * uint64(key)
		offset := uint64(ttsRol16(word)) * uint64(mul)
		if index == 0 {
			term += uint64(cs64Adjustment0) * uint64(factors[0])
			offset += uint64(cs64Adjustment1)
		}
		terms = append(terms, ttsU32(term))
		offsets = append(offsets, ttsU32(offset))
	}

	var swapAcc, swapLast, acc uint64
	var state uint64
	for index := 0; index+1 < len(terms); index += 2 {
		x, y := uint64(terms[index]), uint64(terms[index+1])
		t := uint64(ttsRol16(ttsU32(uint64(offsets[index]) + x)))
		q := t * 0x76F50F34
		swapAcc += uint64(ttsU32(uint64(ttsRol16(ttsU32(q)))*0xC3EFEA97 + (q & 0xFFFFFFFF)))
		t = uint64(ttsRol16(ttsU32(uint64(offsets[index+1]) + y)))
		q = t * 0x1881CE3C
		swapLast = uint64(ttsU32(uint64(ttsRol16(ttsU32(q)))*0x35BD1EC9 + (q & 0xFFFFFFFF)))
		swapAcc = (swapAcc + swapLast) & 0xFFFFFFFF

		state = (state*uint64(keyA) + x) & 0xFFFFFFFF
		state = (uint64(ttsRol16(uint32(state))) * 0xCF98B111) & 0xFFFFFFFF
		state = (uint64(ttsRol16(uint32(state))) * 0x87085B9F) & 0xFFFFFFFF
		state = (uint64(ttsRol16(uint32(state))) * 0x12CEB96D) & 0xFFFFFFFF
		state = (uint64(ttsRol16(uint32(state))) * 0x257E1D83) & 0xFFFFFFFF
		acc += state
		t = (state*uint64(keyB) + y) & 0xFFFFFFFF
		t = (uint64(ttsRol16(uint32(t))) * 0xA27416F5) & 0xFFFFFFFF
		t = (uint64(ttsRol16(uint32(t))) * 0xD38396FF) & 0xFFFFFFFF
		inner := (uint64(ttsRol16(uint32(t))) * 0x7C932B89) & 0xFFFFFFFF
		state = (uint64(ttsRol16(uint32(inner))) * 0xBFA49F69) & 0xFFFFFFFF
		acc = (acc + state) & 0xFFFFFFFF
	}
	return uint32(swapLast) ^ uint32(state), uint32(swapAcc) ^ uint32(acc)
}

// ttsTokenHash mengembalikan (cs64 heks 16, sha256 heks 64) huruf besar.
func ttsTokenHash(data []byte) (string, string) {
	digest := ttsTokenSHA256(data)
	a, b := ttsCS64Digest(data, digest)
	var cs64 [8]byte
	binary.LittleEndian.PutUint32(cs64[0:4], a)
	binary.LittleEndian.PutUint32(cs64[4:8], b)
	return hexUpper(cs64[:]), hexUpper(digest)
}

// hexUpper meng-encode byte jadi heks huruf besar (format token Edge).
func hexUpper(b []byte) string {
	out := make([]byte, hex.EncodedLen(len(b)))
	hex.Encode(out, b)
	for i, c := range out {
		if c >= 'a' && c <= 'f' {
			out[i] = c - 'a' + 'A'
		}
	}
	return string(out)
}

// ttsGECv2 membuat token Sec-MS-GEC v2 untuk waktu Unix tertentu (detik,
// boleh pecahan). Waktu dibulatkan ke 5 menit seperti edge-tts, lalu diubah
// ke tick 100ns epoch Windows.
func ttsGECv2(unix float64) string {
	ticks := unix + ttsWinEpoch
	ticks -= math.Mod(ticks, 300)
	ticks *= 1e9 / 100
	raw := strconv.FormatFloat(ticks, 'f', 0, 64) + ttsTrustedClientToken
	cs64, digest := ttsTokenHash([]byte(raw))
	return cs64 + "-" + digest
}
