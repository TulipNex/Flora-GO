package ai

// Content merepresentasikan satu giliran percakapan (user, model, atau
// hasil tool). Sejak Fase 3, satu Content bisa berisi campuran Part teks
// dan Part function-call/function-response.
type Content struct {
	Role  string `json:"role"` // "user" | "model"
	Parts []Part `json:"parts"`
}

// Part adalah satu potong isi dalam sebuah Content. Sejak Fase 3, selain
// Text (dipakai sejak Fase 1), Part bisa berisi FunctionCall (permintaan
// tool dari model) atau FunctionResponse (hasil tool yang kita kirim balik).
//
// ThoughtSignature: peninggalan jalur Gemini yang sudah dihapus —
// dipertahankan sebagai field agar histori lama yang masih menyimpannya
// tetap bisa di-unmarshal, tapi tidak lagi diisi maupun dibaca oleh
// provider mana pun (NexaRouter/Ciora tidak mengenal konsep ini).
type Part struct {
	Text             string            `json:"text,omitempty"`
	FunctionCall     *FunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *FunctionResponse `json:"functionResponse,omitempty"`
	ThoughtSignature string            `json:"thoughtSignature,omitempty"`
	// ImageBytes/ImageMime membawa gambar mentah untuk vision model.
	// Tag json:"-" WAJIB: byte gambar tidak boleh ikut ke penyimpanan
	// sesi/DB — hanya adapter OpenAI-compatible yang membaca field ini dan
	// mengubahnya jadi blok image_url base64.
	// Diisi HANYA pada pesan user terbaru yang dikirim (ephemeral, tidak
	// pernah disimpan ke histori), jadi tidak membengkakkan memori/DB.
	ImageBytes []byte `json:"-"`
	ImageMime  string `json:"-"`
}

// FunctionCall adalah permintaan pemanggilan tool dari model.
//
// ID adalah tool_call id provider OpenAI-compatible (mis. "call_xyz") —
// WAJIB di-echo balik persis sama sebagai tool_call_id pada balasan
// role="tool", kalau tidak provider ketat menolak dengan 400. Tag json:"-"
// karena field ini murni dipakai internal Go (dibaca lewat
// ToolCall.ID/loop.go), bukan bagian dari body JSON request.
type FunctionCall struct {
	ID   string                 `json:"-"`
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

// FunctionResponse adalah hasil eksekusi tool yang kita kirim balik ke provider.
//
// ID: lihat catatan pada FunctionCall — diteruskan apa adanya supaya
// openai_common.go bisa memasangkan balasan ini ke tool_call_id asli
// yang benar. Tag json:"-" dengan alasan sama seperti FunctionCall.
type FunctionResponse struct {
	ID       string                 `json:"-"`
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
}

// FunctionDecl adalah skema satu tool yang diberitahukan ke provider.
type FunctionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  ParameterSchema `json:"parameters"`
}

// ParameterSchema adalah skema JSON-Schema-like untuk memvalidasi
// argumen tool.
type ParameterSchema struct {
	Type       string                    `json:"type"` // selalu "OBJECT" untuk root
	Properties map[string]PropertySchema `json:"properties"`
	Required   []string                  `json:"required,omitempty"`
}

// PropertySchema adalah skema satu parameter tool.
type PropertySchema struct {
	Type        string          `json:"type"` // "STRING", "NUMBER", "BOOLEAN", "ARRAY"
	Description string          `json:"description,omitempty"`
	Items       *PropertySchema `json:"items,omitempty"`
}
