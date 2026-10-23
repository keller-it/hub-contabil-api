#!/usr/bin/env bash
# Hub Contábil — API /v1 em cURL.
# Uso:  export HUB_API_KEY=hk_test_…   (ou hk_live_…)
#       bash hub-api.sh
set -euo pipefail

BASE_URL="${HUB_API_URL:-https://api.hub-contabil.com}"
API_KEY="${HUB_API_KEY:?Defina HUB_API_KEY=hk_test_... (ou hk_live_...)}"
AUTH=(-H "Authorization: Bearer ${API_KEY}" -H "Content-Type: application/json")

# ── 1) Empresas da conta (pega o company_id) ────────────────────────────────
curl -sS "${BASE_URL}/v1/companies" "${AUTH[@]}" | jq .
COMPANY_ID="${COMPANY_ID:-<uuid-da-empresa>}"

# ── 2) Emitir NFS-e (idempotente por external_ref) ──────────────────────────
# 201 = emitida | 200 {"replayed":true} = já existia | 409 IN_PROGRESS | 422 REJECTED
curl -sS -X POST "${BASE_URL}/v1/invoices" "${AUTH[@]}" -d '{
  "company_id": "'"${COMPANY_ID}"'",
  "external_ref": "pedido-12345",
  "competencia": "2026-10-23",
  "tomador": {
    "tipo_documento": "CNPJ",
    "documento": "11222333000144",
    "nome": "Cliente Exemplo LTDA",
    "email": "cliente@example.com"
  },
  "servico": {
    "codigo_tributacao_nacional": "010101",
    "descricao": "Serviço de desenvolvimento de software"
  },
  "valores": { "valor_servico": 1500.00, "iss_retido": false }
}' | jq .

INVOICE_ID="${INVOICE_ID:-<uuid-da-nota>}"

# ── 3) Detalhe + URL assinada do XML/PDF (válida por 60 min) ────────────────
curl -sS "${BASE_URL}/v1/invoices/${INVOICE_ID}" "${AUTH[@]}" | jq .
curl -sS "${BASE_URL}/v1/invoices/${INVOICE_ID}/files?type=xml" "${AUTH[@]}" | jq .
curl -sS "${BASE_URL}/v1/invoices/${INVOICE_ID}/files?type=pdf" "${AUTH[@]}" | jq .
# Baixar:  curl -sS "<url retornada>" -o nota.xml   (sem Authorization)

# ── 4) Listar notas (paginação por cursor) ───────────────────────────────────
curl -sS "${BASE_URL}/v1/invoices?company_id=${COMPANY_ID}&limit=25&status=autorizada" \
  "${AUTH[@]}" | jq .
# próxima página: acrescente &cursor=<next_cursor> até has_more=false

# ── 5) Reenviar por e-mail ───────────────────────────────────────────────────
curl -sS -X POST "${BASE_URL}/v1/invoices/${INVOICE_ID}/resend" \
  "${AUTH[@]}" -d '{"email":"outra-pessoa@example.com"}' | jq .

# ── 6) Cancelar (motivo com 15+ caracteres) ──────────────────────────────────
curl -sS -X POST "${BASE_URL}/v1/invoices/${INVOICE_ID}/cancel" "${AUTH[@]}" -d '{
  "codigo_motivo": "2",
  "motivo": "Serviço não prestado conforme contrato."
}' | jq .

# ── 7) Recorrência mensal, importar XML e carteira ───────────────────────────
curl -sS "${BASE_URL}/v1/wallet" "${AUTH[@]}" | jq .

curl -sS -X POST "${BASE_URL}/v1/recurrences" "${AUTH[@]}" -d '{
  "day_of_month": 5,
  "delivery_email": "cliente@example.com",
  "source": { "type": "issued", "id": "'"${INVOICE_ID}"'" }
}' | jq .

curl -sS -X POST "${BASE_URL}/v1/imports" "${AUTH[@]}" \
  -d '{"xml_base64": "'"$(base64 < nota.xml 2>/dev/null || echo '<base64-do-xml>')"'"}' | jq .

# ── 8) Verificar assinatura de um webhook recebido ───────────────────────────
# X-Hub-Signature: t=<unix>,v1=<hex>; v1 = HMAC-SHA256(secret, "<t>.<body-cru>")
verify_signature() {
  local header="$1" body="$2" secret="$3"
  local t v1 expected
  t=$(printf '%s' "$header" | tr ',' '\n' | sed -n 's/^t=//p')
  v1=$(printf '%s' "$header" | tr ',' '\n' | sed -n 's/^v1=//p')
  expected=$(printf '%s' "${t}.${body}" | openssl dgst -sha256 -hmac "$secret" -hex | awk '{print $NF}')
  [ "$expected" = "$v1" ]
}
# verify_signature "$X_HUB_SIGNATURE" "$RAW_BODY" "$HUB_WEBHOOK_SECRET"
