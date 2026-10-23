// Hub Contábil — API /v1 em Go (stdlib only).
// Uso:   export HUB_API_KEY=hk_test_…   (ou hk_live_…)
//        go run main.go
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	baseURL = getenv("HUB_API_URL", "https://api.hub-contabil.com")
	apiKey  = os.Getenv("HUB_API_KEY") // hk_test_… ou hk_live_…
	client  = &http.Client{Timeout: 30 * time.Second}
)

// APIError carrega o envelope {error:{code,message,request_id}}.
type APIError struct {
	Status    int
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func (e *APIError) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message) }

// do: sucesso → body decodificado em out; falha → *APIError.
func do(ctx context.Context, method, path string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode >= 400 {
		var env struct {
			Error APIError `json:"error"`
		}
		_ = json.Unmarshal(raw, &env)
		env.Error.Status = res.StatusCode
		return &env.Error
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

type Company struct {
	ID          string  `json:"id"`
	CNPJ        string  `json:"cnpj"`
	RazaoSocial string  `json:"razao_social"`
	CodMunicipio *string `json:"cod_municipio"`
	InscricaoMunicipal *string `json:"inscricao_municipal"`
}

type Invoice struct {
	ID          string  `json:"id"`
	CompanyID   string  `json:"company_id"`
	Status      string  `json:"status"`
	Livemode    bool    `json:"livemode"`
	NumeroNfse  *string `json:"numero_nfse"`
	ChaveAcesso *string `json:"chave_acesso"`
	ExternalRef *string `json:"external_ref"`
}

func listCompanies(ctx context.Context) ([]Company, error) {
	var res struct {
		Data []Company `json:"data"`
	}
	err := do(ctx, http.MethodGet, "/v1/companies", nil, &res)
	return res.Data, err
}

// EmitInvoice: external_ref = chave de idempotência (obrigatório em produção).
func emitInvoice(ctx context.Context, payload map[string]any) (*Invoice, bool, error) {
	var res struct {
		Invoice     *Invoice `json:"invoice"`
		Replayed    bool     `json:"replayed"`
		EmailStatus *string  `json:"email_status"`
	}
	err := do(ctx, http.MethodPost, "/v1/invoices", payload, &res)
	return res.Invoice, res.Replayed, err
}

func getFileURL(ctx context.Context, id, kind string) (string, error) {
	var res struct {
		URL string `json:"url"`
	}
	err := do(ctx, http.MethodGet, fmt.Sprintf("/v1/invoices/%s/files?type=%s", id, kind), nil, &res)
	return res.URL, err
}

func cancelInvoice(ctx context.Context, id, motivo, codigoMotivo string) error {
	// motivo: 15–255 caracteres; codigo 1=erro na emissão, 2=serviço não prestado, 9=outros
	return do(ctx, http.MethodPost, fmt.Sprintf("/v1/invoices/%s/cancel", id),
		map[string]any{"codigo_motivo": codigoMotivo, "motivo": motivo}, nil)
}

// ── Webhook: verificação de assinatura ─────────────────────────────────────
// Header "X-Hub-Signature: t=<unix>,v1=<hex>"; v1 = HMAC-SHA256(secret, "<t>.<corpo-cru>")
func verifyWebhookSignature(secret, signatureHeader string, rawBody []byte, tolerance time.Duration) bool {
	var t int64
	var v1 string
	for _, kv := range strings.Split(signatureHeader, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			t, _ = strconv.ParseInt(v, 10, 64)
		case "v1":
			v1 = v
		}
	}
	if t == 0 || v1 == "" {
		return false
	}
	if d := time.Since(time.Unix(t, 0)); d > tolerance || d < -tolerance {
		return false // anti-replay
	}
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", t)
	mac.Write(rawBody)
	expected := mac.Sum(nil)
	got, err := hex.DecodeString(v1)
	if err != nil {
		return false
	}
	return hmac.Equal(expected, got)
}

// ── Receptor de webhook (net/http) ─────────────────────────────────────────
func webhookHandler(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body) // corpo cru — nunca re-serialize
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	sig := r.Header.Get("X-Hub-Signature")
	if !verifyWebhookSignature(os.Getenv("HUB_WEBHOOK_SECRET"), sig, raw, 5*time.Minute) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var event struct {
		Event string         `json:"event"`
		Data  map[string]any `json:"data"`
	}
	_ = json.Unmarshal(raw, &event)
	deliveryID := r.Header.Get("X-Hub-Delivery") // deduplique por este id
	fmt.Println(event.Event, deliveryID)
	w.WriteHeader(http.StatusOK) // responda 2xx rápido; processe assíncrono
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	ctx := context.Background()
	companies, err := listCompanies(ctx)
	if err != nil || len(companies) == 0 {
		fmt.Fprintln(os.Stderr, "empresas:", err)
		os.Exit(1)
	}
	company := companies[0]

	invoice, replayed, err := emitInvoice(ctx, map[string]any{
		"company_id":   company.ID,
		"external_ref": "pedido-12345",
		"competencia":  "2026-10-23",
		"tomador": map[string]any{
			"tipo_documento": "CNPJ",
			"documento":      "11222333000144",
			"nome":           "Cliente Exemplo LTDA",
			"email":          "cliente@example.com",
		},
		"servico": map[string]any{
			"codigo_tributacao_nacional": "010101",
			"descricao":                  "Serviço de desenvolvimento de software",
		},
		"valores": map[string]any{"valor_servico": 1500.0, "iss_retido": false},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "emitir:", err)
		os.Exit(1)
	}
	fmt.Println("replayed:", replayed, "invoice:", invoice.ID)
	url, _ := getFileURL(ctx, invoice.ID, "xml")
	fmt.Println("XML (60min):", url)
	// _ = cancelInvoice(ctx, invoice.ID, "Serviço não prestado conforme contrato.", "2")

	// http.HandleFunc("/webhooks/hub", webhookHandler)
	// _ = http.ListenAndServe(":3000", nil)
}
