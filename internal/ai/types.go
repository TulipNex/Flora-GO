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
// ThoughtSignature: field ini WAJIB diteruskan apa adanya (echo balik)
// ketika sebuah Part berisi FunctionCall dimasukkan ulang ke history pada
// request berikutnya. Gemini (terutama model seri 2.5/3 dengan "thinking")
// mengembalikan field ini di response, dan menolak request dengan error 400
// ("missing thought_signature") kalau kita kirim balik functionCall tanpa
// menyertakan signature yang sama persis. Lihat:
// https://ai.google.dev/gemini-api/docs/thought-signatures
type Part struct {
	Text             string            `json:"text,omitempty"`
	FunctionCall     *FunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *FunctionResponse `json:"functionResponse,omitempty"`
	ThoughtSignature string            `json:"thoughtSignature,omitempty"`
}

// FunctionCall adalah permintaan pemanggilan tool dari model.
//
// ID hanya diisi untuk provider ber-gaya OpenAI (OpenRouter/NexaRouter)
// yang menerbitkan tool_call id sendiri (mis. "call_xyz") — WAJIB di-echo
// balik persis sama sebagai tool_call_id pada balasan role="tool", kalau
// tidak provider ketat menolak dengan 400. Gemini tidak mengenal konsep
// ini, jadi field ini selalu kosong untuk jalur Gemini. Tag json:"-"
// supaya TIDAK PERNAH ikut termarshal ke request Gemini (yang tidak
// mengenal field ini sama sekali) — field ini murni dipakai internal Go,
// dibaca lewat ToolCall.ID/loop.go, bukan bagian dari body JSON request.
type FunctionCall struct {
	ID   string                 `json:"-"`
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

// FunctionResponse adalah hasil eksekusi tool yang kita kirim balik ke provider.
//
// ID: lihat catatan pada FunctionCall — diteruskan apa adanya supaya
// openrouter_adapter.go bisa memasangkan balasan ini ke tool_call_id asli
// yang benar. Tag json:"-" dengan alasan sama seperti FunctionCall.
type FunctionResponse struct {
	ID       string                 `json:"-"`
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
}

// GenerateRequest adalah body request ke endpoint generateContent Gemini.
type GenerateRequest struct {
	Contents          []Content         `json:"contents"`
	SystemInstruction *Content          `json:"systemInstruction,omitempty"`
	Tools             []ToolDeclaration `json:"tools,omitempty"`
}

// ToolDeclaration membungkus daftar function declaration sesuai format Gemini.
type ToolDeclaration struct {
	FunctionDeclarations []FunctionDecl `json:"functionDeclarations"`
}

// FunctionDecl adalah skema satu tool yang diberitahukan ke Gemini.
type FunctionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  ParameterSchema `json:"parameters"`
}

// ParameterSchema adalah skema JSON-Schema-like yang dipakai Gemini
// untuk memvalidasi argumen tool.
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

// --- Struct response Gemini (hanya field yang kita butuhkan) ---

type generateResponse struct {
	Candidates []candidate `json:"candidates"`
	Error      *apiError   `json:"error,omitempty"`
}

type candidate struct {
	Content      Content `json:"content"`
	FinishReason string  `json:"finishReason"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}
