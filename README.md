# Masterplan — Integração com a API pública do Hub Contábil (`/v1`)

Guia único e autocontido para integrar sistemas externos à API pública do Hub
Contábil — emissão de **NFS-e** (Sistema Nacional, SEFIN), consulta, cancelamento,
recorrências, importação de XML, carteira de créditos e webhooks. Escrito para
servir tanto de referência para desenvolvedores quanto de **contexto pronto para
agentes de IA** gerarem a integração em qualquer linguagem.

- **Base URL**: `https://api.hub-contabil.com`
- **Documentação interativa (Redoc)**: `https://app.hub-contabil.com/docs/api`
- **OpenAPI cru** (Postman/Insomnia/codegen): `https://app.hub-contabil.com/docs/api/openapi.yaml`
- Exemplos de código por linguagem: diretório `exemplos/` ao lado deste arquivo
  (cURL, Node.js, Python, PHP, Go).

---

## 1. Autenticação e ambientes

Toda chamada (exceto `GET /v1/meta`) exige uma chave de API no header:

```
Authorization: Bearer hk_live_xxxxxxxx   # produção — emite de verdade, cobra
Authorization: Bearer hk_test_xxxxxxxx   # teste — provider mock, nada vai ao SEFIN, sem custo
```

- Chaves são criadas em **Emissão de NFS-e → Integrações (API)** no app
  (`https://app.hub-contabil.com/nfse-emissao/integracoes`). A chave cheia é
  exibida **uma única vez** — guarde em cofre/secret manager.
- `hk_test_` força `livemode: false`: a nota é gravada e retornada normalmente,
  mas **nada é transmitido ao SEFIN e nenhum crédito é debitado**. Use-a para
  desenvolver e testar a integração inteira antes de ir a produção. Notas de
  teste só podem ser canceladas por chaves de teste (e vice-versa).
- Cada chave carrega **escopos** definidos na criação. Escopo ausente →
  `403 FORBIDDEN`.

| Escopo | Libera |
|---|---|
| `companies:read` | `GET /v1/companies` |
| `nfse:read` | `GET /v1/invoices*`, `GET /v1/invoices/:id/files` |
| `nfse:write` | `POST /v1/invoices`, `POST /v1/invoices/:id/{cancel,resend}` |
| `recurrences:read` | `GET /v1/recurrences` |
| `recurrences:write` | `POST/PATCH/DELETE /v1/recurrences*` |
| `imports:write` | `POST /v1/imports` |
| `wallet:read` | `GET /v1/wallet` |

- A conta precisa ter o produto **`hub-api`** ativo — sem ele, `403 FORBIDDEN`.

## 2. Convenções do contrato

### Erros

Envelope único em qualquer falha:

```json
{ "error": { "code": "INVALID_INPUT", "message": "Informe company_id ou prestador.cnpj (14 dígitos).", "request_id": "…" } }
```

`request_id` também vai no header `X-Request-Id` — inclua-o em logs e ao abrir
suporte. Tabela HTTP ↔ `code`:

| HTTP | `code` | Significado |
|---|---|---|
| 400 | `INVALID_INPUT` | Payload inválido/incompleto |
| 400 | `CERTIFICATE_NOT_CONFIGURED` | Nem certificado efêmero nem cadastrado |
| 400 | `CERTIFICATE_INVALID`, `SIGNATURE_ERROR` | A1 inválido/assinatura falhou |
| 401 | `UNAUTHORIZED` | Chave ausente, inválida ou revogada |
| 402 | `INSUFFICIENT_CREDITS` | Carteira do emissor avulso sem saldo |
| 403 | `FORBIDDEN` | `hub-api` inativo ou escopo ausente |
| 404 | `NOT_FOUND` | Recurso inexistente (ou de outra conta) |
| 409 | `ALREADY_CANCELLED`, `IN_PROGRESS` | Já cancelada / mesma `external_ref` em emissão |
| 422 | `REJECTED` | Rejeição fiscal do SEFIN |
| 429 | `RATE_LIMITED`, `PROVIDER_RATE_LIMITED` | Respeitar header `Retry-After` (segundos) |
| 500 | `INTERNAL` | Erro interno |
| 501 | `NOT_IMPLEMENTED` | — |
| 502 | `PROVIDER_UNAVAILABLE`, `STORAGE_ERROR`, … | Falha upstream |

### Rate limit

**60 requisições/minuto por chave** (por instância do worker — o limite efetivo
pode ser um pouco maior com várias instâncias). Estouro → `429` + `Retry-After`.
Implemente retry com backoff exponencial honrando `Retry-After`.

### Paginação

`GET /v1/invoices` pagina por cursor (`created_at|id`):

- `limit` (1–100, default 25), `cursor` (vem do `next_cursor` da página anterior);
- resposta: `{ data: [...], has_more, next_cursor }` — pare quando `has_more: false`;
- filtros: `company_id`, `status` (`autorizada|cancelada`), `external_ref`,
  `livemode` (`true|false`), `from`/`to` (ISO date-time).

## 3. Idempotência (obrigatória em produção)

`POST /v1/invoices` deduplica por **(empresa, referência)**: use `external_ref`
no corpo **ou** o header `Idempotency-Key` como fallback. Mesma referência na
mesma empresa nunca transmite duas DPS:

| Situação | Resposta |
|---|---|
| Primeira vez | `201` + nota emitida |
| Já emitida com essa ref | `200` com `replayed: true` e a nota original |
| Emissão em andamento | `409 IN_PROGRESS` — aguarde e consulte |
| Tentativa anterior falhou | A chamada reemite a partir da reserva |

**Regra de ouro**: sempre envie `external_ref` estável (id do pedido/pagamento).
Assim, retries de rede/timeout são seguros por construção.

## 4. Endpoints

| Método + path | Escopo | Descrição |
|---|---|---|
| `GET /v1/meta` | — (público) | Catálogo de escopos e eventos de webhook |
| `GET /v1/companies` | `companies:read` | Empresas da conta (`{ data: [{id, cnpj, razao_social, cod_municipio, inscricao_municipal}] }`) |
| `POST /v1/invoices` | `nfse:write` | Emitir NFS-e (síncrono). `201` emitida; `200` replay |
| `GET /v1/invoices` | `nfse:read` | Listar notas (cursor + filtros) |
| `GET /v1/invoices/suggestions?company_id=\|cnpj=` | `nfse:read` | Autopreenchimento: tomadores/serviços do histórico, no formato do `POST` |
| `GET /v1/service-codes?q=` | `nfse:read` | Catálogo LC 116 — acha o `codigo_tributacao_nacional` (`{ data: [{codigo, descricao}] }`) |
| `GET /v1/invoices/:id` | `nfse:read` | Detalhe: `{ invoice }` |
| `GET /v1/invoices/:id/files?type=xml\|pdf` | `nfse:read` | `{ url, expires_in_seconds: 3600 }` — URL assinada temporária |
| `POST /v1/invoices/:id/cancel` | `nfse:write` | Cancelar (evento e101101): `{ cancelled, id }` |
| `POST /v1/invoices/:id/resend` | `nfse:write` | Reenviar por e-mail: `{ status, error }` |
| `POST /v1/imports` | `imports:write` | Importar XML de NFS-e (`xml` ou `xml_base64`) — modelo p/ recorrência/cópia |
| `GET /v1/recurrences?company_id=` | `recurrences:read` | Listar recorrências mensais `{ data: [...] }` |
| `POST /v1/recurrences` | `recurrences:write` | Criar recorrência `{ id }` |
| `PATCH /v1/recurrences/:id` | `recurrences:write` | Ajustar `delivery_email`/`copy_email` |
| `DELETE /v1/recurrences/:id` (ou `POST`) | `recurrences:write` | Cancelar recorrência |
| `GET /v1/wallet` | `wallet:read` | `{ balance_cents, price_per_note_cents, billing_enabled }` |

## 5. Emissão — `POST /v1/invoices`

### Request (`InvoiceEmitRequest`)

```json
{
  "company_id": "uuid-da-empresa",
  "external_ref": "pedido-12345",
  "competencia": "2026-10-23",
  "prestador": {
    "codigo_municipio": "3550308",
    "inscricao_municipal": "12345678",
    "opcao_simples_nacional": 1,
    "regime_especial_tributacao": 0
  },
  "tomador": {
    "tipo_documento": "CNPJ",
    "documento": "11222333000144",
    "nome": "Cliente Exemplo LTDA",
    "email": "cliente@example.com",
    "endereco": { "...": "opcional" }
  },
  "servico": {
    "codigo_tributacao_nacional": "010101",
    "descricao": "Serviço de desenvolvimento de software",
    "codigo_municipio_incidencia": "3550308",
    "informacoes_complementares": "Pedido #12345"
  },
  "valores": {
    "valor_servico": 1500.00,
    "aliquota_iss": 2.9,
    "iss_retido": false
  },
  "certificate": { "pfx_base64": "…", "password": "…" },
  "send_email": true,
  "email_to": "copia@example.com"
}
```

Campos:

- **Empresa prestadora** *(obrigatória, de duas formas)*:
  - **`company_id`** — `id` de `GET /v1/companies`. O cadastro é a fonte de
    verdade: campos `prestador.*` divergentes do cadastro retornam `400`;
  - **`prestador.cnpj`** — sem `company_id`, a empresa é resolvida por
    **find-or-create** na conta (14 dígitos; `razao_social` obrigatória na
    criação). Campos fiscais enviados viram patch do cadastro; ausentes não
    mexem. O pedido fica autossuficiente — ideal para quem integra sem
    cadastrar nada no app.
- **`external_ref`**: sua referência única — chave de idempotência (ver §3).
- **`competencia`** *(obrigatório)*: `YYYY-MM-DD`.
- **`prestador`**: `cnpj`, `razao_social`, `nome_fantasia`,
  `codigo_municipio` (IBGE, 7 dígitos), `inscricao_municipal`,
  `opcao_simples_nacional`, `regime_apuracao_simples`,
  `regime_especial_tributacao`, `fone`, `email`.
- **`tomador`** *(obrigatório)*: `tipo_documento` ∈ `CNPJ|CPF|NIF`, `documento`
  (só dígitos p/ CNPJ/CPF), `nome`, `email`, `fone`, `inscricao_municipal`,
  `motivo_sem_nif` (`0`/`1`/`2` — estrangeiro sem NIF), `endereco`
  (`logradouro`, `numero`, `bairro`, `codigo_municipio`, `uf`, `cep`, ou
  `exterior{codigo_pais,cidade,codigo_postal,estado_provincia_regiao}`).
- **`servico`** *(obrigatório)*: `codigo_tributacao_nacional` — item da LC 116,
  6 dígitos (ex.: `010101` = análise/desenvolvimento de sistemas — ache em
  `GET /v1/service-codes`); `descricao` obrigatória. Opcionais:
  `codigo_tributacao_municipal`, `codigo_nbs`, `codigo_interno`,
  `codigo_municipio_incidencia`, `codigo_obra`, `codigo_cib`,
  `inscricao_imobiliaria_fiscal`, `endereco_obra`, `comercio_exterior`
  (grupo `serv/comExt` do leiaute — **vai dentro de `servico`**, não de
  `valores`), `informacoes_complementares`.
- **`valores`** *(obrigatório)*: `valor_servico` em **reais** (número decimal);
  `desconto_incondicionado`, `desconto_condicionado`, `tributacao_issqn`,
  `aliquota_iss` (%), **`iss_retido` booleano** (`true` = ISS retido na fonte —
  truthy comuns `1`/`"1"`/`2` também viram retido), `total_tributos`,
  `tributos_federais` conforme o schema nacional.
- **`certificate`**: A1 efêmero `{ pfx_base64, password }` — ver §8.
- **`send_email`** (default `true`): envia XML+DANFSe ao tomador; `email_to`
  sobrescreve o destinatário (default: `tomador.email`).

**Convenção de chaves**: o contrato é snake_case, mas objetos aninhados
(`endereco`, `tributos_federais`, `comercio_exterior`, …) aceitam chaves em
camelCase também — as duas formas são normalizadas. Use snake_case.

**Autopreenchimento**: `GET /v1/invoices/suggestions` (`?company_id=` ou
`?cnpj=`) devolve `tomadores` e `servicos` mais recentes/frequentes do
histórico da conta (notas emitidas + NFS-e prestadas do ADN) **já no formato
do `POST /v1/invoices`** — copie o bloco, ajuste valor/competência e emita.

### Response

`201` (emitida) ou `200` (replay idempotente):

```json
{
  "invoice": { "id": "…", "status": "autorizada", "numero_nfse": "…", "…": "…" },
  "replayed": false,
  "email_status": "sent"
}
```

### Objeto `Invoice`

```json
{
  "id": "uuid",
  "company_id": "uuid",
  "status": "autorizada | cancelada",
  "livemode": true,
  "environment": "production | restricted",
  "provider": "sefin | mock",
  "external_ref": "pedido-12345",
  "numero_nfse": "…",
  "chave_acesso": "…",
  "dps": { "serie": "00800", "numero": 123 },
  "tomador": { "tipo_documento": "CNPJ", "documento": "…", "nome": "…", "email": "…" },
  "servico": { "codigo": "010101", "descricao": "…", "codigo_municipio_incidencia": "…" },
  "valor_servico": 1500.0,
  "aliquota_iss": 2.9,
  "iss_retido": 0,
  "data_competencia": "2026-10-23",
  "data_emissao": "2026-10-23T…Z",
  "cancelled_at": null,
  "cancel_reason": null,
  "created_at": "2026-10-23T…Z"
}
```

## 6. Arquivos (XML e PDF/DANFSe)

Paths internos nunca são expostos: `GET /v1/invoices/:id/files?type=xml|pdf`
devolve uma **URL assinada temporária (60 min)** — baixe o arquivo com um GET
simples nessa URL (sem header de auth).

## 7. Cancelamento — `POST /v1/invoices/:id/cancel`

```json
{ "codigo_motivo": "2", "motivo": "Serviço não prestado conforme contrato." }
```

- `codigo_motivo`: `1` = erro na emissão, `2` = serviço não prestado (default),
  `9` = outros.
- `motivo`: **15–255 caracteres** (mínimo exigido pelo schema nacional).
- `certificate` efêmero opcional, mesmo comportamento da emissão.
- Resposta `200 { cancelled: true, id }`. Já cancelada → `409 ALREADY_CANCELLED`.
- Chave de teste só cancela nota emitida em teste (`livemode: false`).

## 8. Certificado A1 do prestador

Emissão e cancelamento em produção exigem o A1, resolvido nesta ordem:

1. **`certificate` no pedido** (`pfx_base64` + `password`): uso **efêmero** —
   descriptografado só em memória naquela chamada, **nunca persistido**. Tem
   prioridade sobre o cadastrado. Teto: 256 KB em base64.
2. **Certificado cadastrado da empresa** no app.

Sem nenhum dos dois → `400 CERTIFICATE_NOT_CONFIGURED`. Chaves `hk_test_`
ignoram o campo (mock não fala com o SEFIN). **`certificate` não é aceito em
`payload` de recorrência** — a emissão mensal usa sempre o certificado
cadastrado.

## 9. Recorrências, importação e carteira

- **Recorrência mensal** — `POST /v1/recurrences`:
  `{ day_of_month: 1–28, payload: <InvoiceEmitRequest> | source: { type: "issued"|"document"|"imported", id }, delivery_email: "obrigatório", copy_email: "opcional" }`.
  Emite todo mês no dia escolhido e manda a nota ao `delivery_email`.
  `PATCH` ajusta e-mails; `DELETE` cancela (para mudar o dia, cancele e recrie).
- **Importar XML** — `POST /v1/imports` com `{ xml: "…" }` **ou**
  `{ xml_base64: "…" }`. Registra o XML como nota importada (modelo para
  copiar/emitir e origem de recorrência).
- **Carteira** — `GET /v1/wallet`: saldo e preço por nota em **centavos**
  (`billing_enabled` indica se a conta cobra por nota — emissor avulso).
  Via API a nota custa **R$0,50** (`price_per_note_cents: 50`) contra
  R$1,00 da emissão avulsa no app.

## 10. Webhooks (Hub → seu sistema)

Endpoints HTTPS públicos cadastrados no painel (**Integrações → Webhooks**)
recebem um `POST` JSON assinado por evento. Não é possível criar webhooks pela
`/v1` — a gestão é pelo app (há endpoint de teste e rotação de segredo no
painel).

### Formato da entrega

Headers:

```
X-Hub-Event: invoice.issued
X-Hub-Delivery: <id da entrega — use para deduplicar>
X-Hub-Signature: t=<unix_timestamp>,v1=<hmac_hex>
Content-Type: application/json
```

Corpo:

```json
{ "id": "evt_…", "event": "invoice.issued", "created_at": "…", "livemode": true, "data": { "…": "…" } }
```

### Verificação da assinatura (fazer sempre)

1. Parse `X-Hub-Signature` → `t` e `v1`.
2. Rejeite se `|agora − t| > 5 min` (anti-replay).
3. `esperado = HMAC_SHA256(secret_whsec, "<t>.<raw_body>")` em hex — use o
   **corpo cru** recebido, nunca o JSON re-serializado.
4. Compare `esperado` com `v1` em **tempo constante**
   (`timingSafeEqual`, `hmac.compare_digest`, `hash_equals`, `hmac.Equal`).

O segredo `whsec_…` é exibido uma vez na criação (rotação no painel).

### Eventos e payloads (`data`)

| Evento | `data` |
|---|---|
| `invoice.issued` | `{ invoice: { id, company_id, status, numero_nfse, chave_acesso, serie, numero_dps, valor_servico, tomador:{nome,documento,email}, data_emissao, external_ref, api_key_id } }` |
| `invoice.cancelled` | `{ invoice: { id, source, document_id, company_id, chave_acesso, motivo, cancelled_at }, api_key_id }` |
| `invoice.failed` | `{ invoice: { id: null, company_id, external_ref }, error: { code, message }, api_key_id }` — emitida quando a emissão falha após a reserva |
| `postbox.message_received` | Caixa Postal RFB (outro produto — aparece em `GET /v1/meta`) |
| `webhook.test` | `{ test: true, endpoint_id }` — botão "testar" do painel |

### Retries e boas práticas do receptor

- Responda **2xx rápido** (processe assíncrono); qualquer resposta ≥400 ou
  timeout (10s) conta como falha.
- Falhas são **retentadas**: backoff `1min → 5min → 30min → 2h → 12h`, até 8
  tentativas ou 24h — entregas podem se repetir → **deduplique por
  `X-Hub-Delivery`** (ou `payload.id`) e trate ordem fora de sequência.
- Endpoint precisa ser **HTTPS público** (sem IP privado/localhost — anti-SSRF).
- Histórico de entregas (status, tentativas, último erro) e botão
  **Reentregar** ficam no painel de Integrações.

## 11. Checklist para agentes de IA implementarem o cliente

Superfície mínima sugerida (nomes ilustrativos):

```
listCompanies()                        → GET /v1/companies
listServiceCodes(q?)                   → GET /v1/service-codes
listSuggestions({company_id?|cnpj?})   → GET /v1/invoices/suggestions
emitInvoice(payload)                   → POST /v1/invoices (sempre external_ref;
                                         company_id OU prestador.cnpj)
getInvoice(id)                         → GET /v1/invoices/:id
listInvoices({filters, cursor, limit}) → GET /v1/invoices (loop até !has_more)
getFileUrl(id, "xml"|"pdf")            → GET /v1/invoices/:id/files
cancelInvoice(id, codigo_motivo, motivo, certificate?) → POST …/cancel
resendInvoice(id, email?)              → POST …/resend
importXml(xml)                          → POST /v1/imports
listRecurrences(company_id?)            → GET /v1/recurrences
createRecurrence({day_of_month, payload|source, delivery_email, copy_email?})
updateRecurrence(id, {delivery_email?, copy_email?})
cancelRecurrence(id)
getWallet()                             → GET /v1/wallet
verifyWebhookSignature(secret, signatureHeader, rawBody, toleranceSec=300) → bool
```

Regras que o código gerado **precisa** seguir:

1. Nunca logar a chave `hk_*` nem o segredo `whsec_*`; ler de env/secret manager.
2. Enviar `external_ref` em toda emissão; tratar `replayed: true` como sucesso.
3. Em `409 IN_PROGRESS`: aguardar e consultar `GET /v1/invoices?external_ref=…`.
4. Em `429`: respeitar `Retry-After`; demais erros: propagar `error.code` +
   `request_id` (não engolir o envelope).
5. Timeout de request (ex.: 30s) + retry só em erro de transporte/`5xx` —
   **nunca** re-emitir `422 REJECTED`/`400` sem corrigir o payload.
6. Webhook: ler o corpo **cru** antes de qualquer parse, verificar assinatura +
   timestamp, dedup por `X-Hub-Delivery`, responder 2xx rápido.
7. `certificate` só em `POST /v1/invoices`/`cancel` com chave `hk_live_` — nunca
   em recorrência; com `hk_test_` é ignorado.
8. `motivo` de cancelamento com ≥15 chars.
9. Separar claramente config `test` × `live` (env var `HUB_API_KEY`).

## 12. Teste de aceitação (com `hk_test_`)

```bash
export HUB_API_KEY=hk_test_…
BASE=https://api.hub-contabil.com

# 1) Empresas → pega o company_id
curl -s "$BASE/v1/companies" -H "Authorization: Bearer $HUB_API_KEY"

# 2) Emite (mock — retorna na hora, livemode:false, sem custo)
curl -s -X POST "$BASE/v1/invoices" \
  -H "Authorization: Bearer $HUB_API_KEY" -H "Content-Type: application/json" \
  -d '{"company_id":"<id>","external_ref":"teste-1","competencia":"2026-10-23",
       "tomador":{"tipo_documento":"CNPJ","documento":"11222333000144","nome":"Teste LTDA","email":"t@example.com"},
       "servico":{"codigo_tributacao_nacional":"010101","descricao":"Teste de integração"},
       "valores":{"valor_servico":100}}'
# esperado: 201 { invoice{livemode:false, numero_nfse:…}, replayed:false }

# 3) Replay: repetir o MESMO POST → 200 { replayed:true } e MESMO invoice.id

# 4) Arquivos
curl -s "$BASE/v1/invoices/<id>/files?type=xml" -H "Authorization: Bearer $HUB_API_KEY"
# → { url, expires_in_seconds: 3600 }

# 5) Cancelamento
curl -s -X POST "$BASE/v1/invoices/<id>/cancel" \
  -H "Authorization: Bearer $HUB_API_KEY" -H "Content-Type: application/json" \
  -d '{"codigo_motivo":"2","motivo":"Teste de cancelamento via API"}'
# → 200 { cancelled:true }; repetir → 409 ALREADY_CANCELLED

# 6) Sem chave → 401 UNAUTHORIZED; 61ª req/min → 429 RATE_LIMITED
```

## 13. Limites e notas finais

- Emissão é **síncrona**: a resposta já traz a nota autorizada — não há polling
  obrigatório (webhook é confirmação/conveniência).
- NFS-e é o **Sistema Nacional** — cobertura municipal segue o adesão da
  prefeitura ao sistema nacional; rejeições fiscais voltam como `422 REJECTED`
  com a mensagem do SEFIN.
- `GET /v1/meta` (sem auth) lista escopos e eventos de webhook vigentes — útil
  para o agente descobrir o catálogo em runtime.
- A API versiona por prefixo (`/v1`); mudanças incompatíveis viram `/v2`.
