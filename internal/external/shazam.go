package external

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Shazam song recognition — port dari shazam.mjs (sidik-jari format
// proprietary Apple via amp.shazam.com, tanpa API key) dengan algoritma
// DSP + encoder biner meniru goshazam/ShazamIO.
//
// Alur recognize: audio apa pun → ffmpeg jadi PCM s16le mono 16kHz →
// peak-picking FFT → hash kombinatorial → encoder biner Apple → POST
// discovery → track/matches. Kebenaran format HANYA terbukti lewat live
// test (server balas track = format tepat).

const (
	shazamSampleRate   = 16000
	shazamMaxSeconds   = 12 // sama dengan default gist (seconds=12)
	shazamFFTSize      = 2048
	shazamFFTOutSize   = shazamFFTSize/2 + 1
	shazamNumFFTs      = 256
	shazamMinPeakMag   = 1.0 / 64.0
	shazamMagic1       = 0xCAFE2580
	shazamMagic2       = 0x94119C00
	shazamFixedValue   = (15 << 19) + 0x40000
	shazamDiscoveryURL = "https://amp.shazam.com/discovery/v5/%s/%s/android/-/tag/%s/%s?sync=true&webv3=true&sampling=true&connected=&shazamapiversion=v3&sharehub=true&video=v3"
	shazamUA           = "Shazam/14.1.0 (iPhone; iOS 14.4; Scale/3.00)"
)

// ShazamTrack adalah hasil normalisasi recognize.
type ShazamTrack struct {
	ID          string
	Title       string
	Artist      string
	Album       string
	Label       string
	ReleaseYear string
	Genres      string
	Cover       string
	ShareURL    string
	AppleURL    string
	SpotifyURL  string
	HasLyrics   bool
	Lyrics      string
}

// shazamFreqBand adalah pita frekuensi signature Apple.
type shazamFreqBand int

const (
	shazamBand250_520 shazamFreqBand = iota
	shazamBand520_1450
	shazamBand1450_3500
	shazamBand3500_5500
)

type shazamPeak struct {
	PassNumber uint32
	Magnitude  float64
	FreqBin    uint16
}

// shazamFFT radix-2 iteratif untuk input real (panjang harus pangkat 2).
// Mengembalikan N/2+1 koefisien kompleks (cermin gonum Coefficients).
func shazamFFT(x []float64) []complex128 {
	n := len(x)
	a := make([]complex128, n)
	for i, v := range x {
		a[i] = complex(v, 0)
	}
	// Bit-reversal.
	j := 0
	for i := 1; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j &= ^bit
		}
		j |= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	// Butterflies.
	for size := 2; size <= n; size <<= 1 {
		ang := -2 * math.Pi / float64(size)
		wSize := complex(math.Cos(ang), math.Sin(ang))
		for i := 0; i < n; i += size {
			w := complex(1, 0)
			for k := 0; k < size/2; k++ {
				u := a[i+k]
				v := a[i+k+size/2] * w
				a[i+k] = u + v
				a[i+k+size/2] = u - v
				w *= wSize
			}
		}
	}
	return a[:n/2+1]
}

type shazamSigGen struct {
	ring       []int16
	reordered  []float64
	fftOut     [][]float64
	spread     [][]float64
	ringIdx    int
	fftIdx     int
	spreadIdx  int
	numSpread  uint32
	peaks      map[shazamFreqBand][]shazamPeak
	numSamples uint32
	hann       []float64
}

func newShazamSigGen() *shazamSigGen {
	s := &shazamSigGen{
		ring:      make([]int16, shazamFFTSize),
		reordered: make([]float64, shazamFFTSize),
		fftOut:    make([][]float64, shazamNumFFTs),
		spread:    make([][]float64, shazamNumFFTs),
		peaks:     make(map[shazamFreqBand][]shazamPeak),
		hann:      make([]float64, shazamFFTSize),
	}
	for i := range s.hann {
		s.hann[i] = 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(shazamFFTSize-1)))
	}
	for i := range s.fftOut {
		s.fftOut[i] = make([]float64, shazamFFTOutSize)
		s.spread[i] = make([]float64, shazamFFTOutSize)
	}
	return s
}

func (s *shazamSigGen) doFFT(chunk []int16) {
	for i := 0; i < len(chunk); i++ {
		s.ring[(s.ringIdx+i)%shazamFFTSize] = chunk[i]
	}
	s.ringIdx = (s.ringIdx + len(chunk)) % shazamFFTSize
	for i := 0; i < shazamFFTSize; i++ {
		s.reordered[i] = float64(s.ring[(s.ringIdx+i)%shazamFFTSize]) * s.hann[i]
	}
	coeffs := shazamFFT(s.reordered)
	out := s.fftOut[s.fftIdx]
	for i := 0; i < shazamFFTOutSize; i++ {
		mag := (real(coeffs[i])*real(coeffs[i]) + imag(coeffs[i])*imag(coeffs[i])) / float64(1<<17)
		out[i] = math.Max(mag, 1e-10)
	}
	s.fftIdx = (s.fftIdx + 1) % shazamNumFFTs
}

func (s *shazamSigGen) doPeakSpreading() {
	last := s.fftOut[(s.fftIdx-1+shazamNumFFTs)%shazamNumFFTs]
	t0 := append([]float64(nil), last...)
	t1 := append(append([]float64(nil), last[1:]...), last[0])
	t2 := append(append([]float64(nil), last[2:]...), last[:2]...)
	sp := make([]float64, shazamFFTOutSize)
	for i := 0; i < shazamFFTOutSize-3; i++ {
		sp[i] = math.Max(t0[i], math.Max(t1[i], t2[i]))
	}
	copy(sp[shazamFFTOutSize-3:], last[shazamFFTOutSize-3:])
	prev := sp
	for _, off := range []int{-1, -3, -6} {
		pos := (s.spreadIdx + off + shazamNumFFTs) % shazamNumFFTs
		cur := s.spread[pos]
		for j := 0; j < shazamFFTOutSize; j++ {
			if prev[j] > cur[j] {
				cur[j] = prev[j]
			}
		}
		prev = cur
	}
	s.spread[s.spreadIdx] = sp
	s.spreadIdx = (s.spreadIdx + 1) % shazamNumFFTs
}

func (s *shazamSigGen) doPeakRecognition() {
	minus46 := s.fftOut[(s.fftIdx-46+shazamNumFFTs)%shazamNumFFTs]
	minus49 := s.spread[(s.spreadIdx-49+shazamNumFFTs)%shazamNumFFTs]
	for bin := 10; bin <= 1014; bin++ {
		if minus46[bin] < shazamMinPeakMag || minus46[bin] < minus49[bin-1] {
			continue
		}
		maxN := 0.0
		for _, off := range []int{-10, -7, -4, -3, 1, 2, 5, 8} {
			if idx := bin + off; idx >= 0 && idx < shazamFFTOutSize {
				maxN = math.Max(maxN, minus49[idx])
			}
		}
		if minus46[bin] <= maxN {
			continue
		}
		maxOther := maxN
		for _, off := range []int{-53, -45, 165, 172, 179, 186, 193, 200, 214, 221, 228, 235, 242, 249} {
			other := s.spread[(s.spreadIdx+off+shazamNumFFTs)%shazamNumFFTs]
			if bin-1 >= 0 && bin-1 < shazamFFTOutSize {
				maxOther = math.Max(maxOther, other[bin-1])
			}
		}
		if minus46[bin] <= maxOther {
			continue
		}
		mag := math.Log(math.Max(shazamMinPeakMag, minus46[bin]))*1477.4 + 6144.0
		magBefore := math.Log(math.Max(shazamMinPeakMag, minus46[bin-1]))*1477.4 + 6144.0
		magAfter := math.Log(math.Max(shazamMinPeakMag, minus46[bin+1]))*1477.4 + 6144.0
		var1 := mag*2.0 - magBefore - magAfter
		if var1 <= 0 {
			continue
		}
		var2 := (magAfter - magBefore) * 32.0 / var1
		binCorr := uint16(bin*64) + uint16(var2+0.5)
		hz := float64(binCorr) * (16000.0 / 2.0 / 1024.0 / 64.0)
		var band shazamFreqBand
		switch {
		case hz > 250 && hz < 520:
			band = shazamBand250_520
		case hz >= 520 && hz < 1450:
			band = shazamBand520_1450
		case hz >= 1450 && hz < 3500:
			band = shazamBand1450_3500
		case hz >= 3500 && hz <= 5500:
			band = shazamBand3500_5500
		default:
			continue
		}
		if band == shazamBand3500_5500 {
			continue
		}
		s.peaks[band] = append(s.peaks[band], shazamPeak{
			PassNumber: s.numSpread - 46,
			Magnitude:  mag,
			FreqBin:    binCorr,
		})
	}
}

// shazamSignatureURI membangun data URI signature Apple dari PCM mono 16kHz.
func shazamSignatureURI(samples []int16) (string, int, error) {
	maxSamples := shazamMaxSeconds * shazamSampleRate
	if len(samples) > maxSamples {
		samples = samples[:maxSamples]
	}
	gen := newShazamSigGen()
	gen.numSamples = uint32(len(samples))
	for i := 0; i+128 <= len(samples); i += 128 {
		gen.doFFT(samples[i : i+128])
		gen.doPeakSpreading()
		gen.numSpread++
		if gen.numSpread >= 46 {
			gen.doPeakRecognition()
		}
	}
	bands := make([]shazamFreqBand, 0, len(gen.peaks))
	for b := range gen.peaks {
		bands = append(bands, b)
	}
	sort.Slice(bands, func(i, j int) bool { return bands[i] < bands[j] })

	var contents bytes.Buffer
	for _, band := range bands {
		var pb bytes.Buffer
		var pass uint32
		for _, p := range gen.peaks[band] {
			if p.PassNumber-pass >= 255 {
				pb.WriteByte(0xff)
				_ = binary.Write(&pb, binary.LittleEndian, p.PassNumber)
				pass = p.PassNumber
			}
			pb.WriteByte(byte(p.PassNumber - pass))
			_ = binary.Write(&pb, binary.LittleEndian, uint16(p.Magnitude))
			_ = binary.Write(&pb, binary.LittleEndian, p.FreqBin)
			pass = p.PassNumber
		}
		peakBytes := pb.Bytes()
		_ = binary.Write(&contents, binary.LittleEndian, uint32(0x60030040+uint32(band)))
		_ = binary.Write(&contents, binary.LittleEndian, uint32(len(peakBytes)))
		contents.Write(peakBytes)
		contents.Write(make([]byte, (4-len(peakBytes)%4)%4))
	}
	sizeMinusHeader := uint32(contents.Len() + 8)

	var hdr bytes.Buffer
	_ = binary.Write(&hdr, binary.LittleEndian, uint32(shazamMagic1))
	_ = binary.Write(&hdr, binary.LittleEndian, uint32(0)) // crc placeholder
	_ = binary.Write(&hdr, binary.LittleEndian, sizeMinusHeader)
	_ = binary.Write(&hdr, binary.LittleEndian, uint32(shazamMagic2))
	hdr.Write(make([]byte, 12))
	shifted := map[uint32]uint32{8000: 1 << 27, 11025: 2 << 27, 16000: 3 << 27, 32000: 4 << 27, 44100: 5 << 27, 48000: 6 << 27}[16000]
	_ = binary.Write(&hdr, binary.LittleEndian, shifted)
	hdr.Write(make([]byte, 8))
	_ = binary.Write(&hdr, binary.LittleEndian, uint32(len(samples))+uint32(float32(16000)*0.24))
	_ = binary.Write(&hdr, binary.LittleEndian, uint32(shazamFixedValue))

	var buf bytes.Buffer
	buf.Write(hdr.Bytes())
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0x40000000))
	_ = binary.Write(&buf, binary.LittleEndian, sizeMinusHeader)
	buf.Write(contents.Bytes())

	full := buf.Bytes()
	crc := crc32.ChecksumIEEE(full[8:])
	binary.LittleEndian.PutUint32(full[4:8], crc)
	samplems := int(float32(len(samples)) / float32(shazamSampleRate) * 1000)
	return "data:audio/vnd.shazam.sig;base64," + base64.StdEncoding.EncodeToString(full), samplems, nil
}

// DecodeAudioPCM16K memakai ffmpeg (sudah wajib ada) untuk mengubah audio
// apa pun menjadi PCM s16le mono 16kHz via stdin pipe (tanpa file temp).
func DecodeAudioPCM16K(ctx context.Context, data []byte, isVideo bool) ([]int16, error) {
	args := []string{"-v", "error", "-i", "pipe:0"}
	if isVideo {
		args = append(args, "-vn")
	}
	args = append(args, "-f", "s16le", "-acodec", "pcm_s16le", "-ar", "16000", "-ac", "1", "pipe:1")
	tctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd := exec.CommandContext(tctx, "ffmpeg", args...)
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("shazam: ffmpeg: %v (%s)", err, stderr.String())
	}
	raw := out.Bytes()
	if len(raw) < 32000 { // <1 detik @16kHz mono s16 = 32KB
		return nil, fmt.Errorf("shazam: audio terlalu pendek (<1 detik)")
	}
	n := len(raw) / 2
	samples := make([]int16, n)
	for i := 0; i < n; i++ {
		samples[i] = int16(raw[2*i]) | int16(raw[2*i+1])<<8
	}
	return samples, nil
}

func shazamReqID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	n := 100000 + (int(b[0])<<16|int(b[1])<<8|int(b[2]))%900000
	return fmt.Sprintf("%d", n)
}

// shazamTrackRaw adalah subset respons discovery/track yang dipakai.
type shazamTrackRaw struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	URL      string `json:"url"`
	Images   *struct {
		CoverArt   string `json:"coverart"`
		CoverArtHQ string `json:"coverarthq"`
	} `json:"images"`
	Share *struct {
		Href string `json:"href"`
	} `json:"share"`
	Genres *struct {
		Primary string `json:"primary"`
	} `json:"genres"`
	Hub *struct {
		Options []struct {
			Actions []struct {
				URI string `json:"uri"`
			} `json:"actions"`
		} `json:"options"`
		Providers []struct {
			Type    string `json:"type"`
			Actions []struct {
				URI string `json:"uri"`
			} `json:"actions"`
		} `json:"providers"`
	} `json:"hub"`
	Sections []struct {
		Type     string   `json:"type"`
		Text     []string `json:"text"`
		Metadata []struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		} `json:"metadata"`
	} `json:"sections"`
}

func normalizeShazamTrack(t *shazamTrackRaw) *ShazamTrack {
	if t == nil {
		return nil
	}
	out := &ShazamTrack{
		ID: t.Key, Title: t.Title, Artist: t.Subtitle,
	}
	if t.Images != nil {
		out.Cover = firstNonEmpty(t.Images.CoverArtHQ, t.Images.CoverArt)
	}
	if t.Share != nil {
		out.ShareURL = t.Share.Href
	}
	if t.Genres != nil {
		out.Genres = t.Genres.Primary
	}
	if t.Hub != nil {
		if len(t.Hub.Options) > 0 && len(t.Hub.Options[0].Actions) > 0 {
			out.AppleURL = t.Hub.Options[0].Actions[0].URI
		}
		if out.AppleURL == "" {
			out.AppleURL = t.URL
		}
		for _, p := range t.Hub.Providers {
			if strings.EqualFold(p.Type, "SPOTIFY") && len(p.Actions) > 0 && p.Actions[0].URI != "" {
				out.SpotifyURL = p.Actions[0].URI
				break
			}
		}
	}
	for _, s := range t.Sections {
		if s.Type == "SONG" {
			for _, m := range s.Metadata {
				switch strings.ToLower(m.Title) {
				case "album":
					out.Album = m.Text
				case "label":
					out.Label = m.Text
				case "released", "dirilis":
					out.ReleaseYear = m.Text
				}
			}
		}
		if s.Type == "LYRICS" && len(s.Text) > 0 {
			out.Lyrics = strings.Join(s.Text, "\n")
			out.HasLyrics = true
		}
	}
	return out
}

// RecognizeShazam mengidentifikasi lagu dari PCM mono 16kHz.
func RecognizeShazam(ctx context.Context, samples []int16) (*ShazamTrack, error) {
	const engine = "shazam"
	uri, samplems, err := shazamSignatureURI(samples)
	if err != nil {
		return nil, err
	}
	uuid1 := strings.ToUpper(uuid.New().String())
	uuid2 := strings.ToLower(uuid.New().String())
	api := fmt.Sprintf(shazamDiscoveryURL, "id-ID", "ID", uuid1, uuid2)
	payload := map[string]any{
		"timezone":    "Asia/Jakarta",
		"signature":   map[string]any{"uri": uri, "samplems": samplems},
		"timestamp":   time.Now().UnixMilli(),
		"context":     map[string]any{},
		"geolocation": map[string]any{},
	}
	body, _ := json.Marshal(payload)
	tctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(tctx, http.MethodPost, api, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", shazamUA)
	req.Header.Set("X-Shazam-Platform", "IPHONE")
	req.Header.Set("X-Shazam-AppVersion", "14.1.0")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Language", "id_ID")
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", engine, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: HTTP %d", engine, resp.StatusCode)
	}
	var r struct {
		Track *shazamTrackRaw `json:"track"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("%s: JSON invalid: %w", engine, err)
	}
	if r.Track == nil {
		return nil, fmt.Errorf("%s: lagu tidak ditemukan di database Shazam", engine)
	}
	return normalizeShazamTrack(r.Track), nil
}
