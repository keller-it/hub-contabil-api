// Hub Contábil — API /v1 em Node.js ≥ 18 (fetch nativo, sem dependências).
// Uso:   export HUB_API_KEY=hk_test_…   (ou hk_live_…)
//        node hub-api.mjs
// Webhook: export HUB_WEBHOOK_SECRET=whsec_…  e descomente o servidor no final.

import { createHmac, timingSafeEqual } from 'node:crypto';
import { createServer } from 'node:http';

const BASE_URL = process.env.HUB_API_URL ?? 'https://api.hub-contabil.com';
const API_KEY = process.env.HUB_API_KEY; // hk_test_… ou hk_live_…

class HubApiError extends Error {
  constructor(status, { code = 'UNKNOWN', message = '', request_id } = {}) {
    super(`${status} ${code}: ${message}`);
    this.name = 'HubApiError';
    this.status = status;
    this.code = code;
    this.requestId = request_id;
  }
}

// Envelope: sucesso → JSON; falha → HubApiError{status, code, message, requestId}
async function api(path, { method = 'GET', body } = {}) {
  const res = await fetch(`${BASE_URL}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${API_KEY}`,
      ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
    signal: AbortSignal.timeout(30_000),
  });
  const data = await res.json().catch(() => null);
  if (!res.ok) {
    const err = (data && data.error) || {};
    const retryAfter = res.headers.get('retry-after');
    if (res.status === 429 && retryAfter) err.retry_after_seconds = Number(retryAfter);
    throw new HubApiError(res.status, err);
  }
  return data;
}

const listCompanies = () => api('/v1/companies').then((r) => r.data);
const getInvoice = (id) => api(`/v1/invoices/${id}`).then((r) => r.invoice);
const getFileUrl = (id, type = 'xml') => api(`/v1/invoices/${id}/files?type=${type}`).then((r) => r.url);
const cancelInvoice = (id, motivo, codigoMotivo = '2') =>
  api(`/v1/invoices/${id}/cancel`, { method: 'POST', body: { codigo_motivo: codigoMotivo, motivo } });
const resendInvoice = (id, email) =>
  api(`/v1/invoices/${id}/resend`, { method: 'POST', body: email ? { email } : {} });
const getWallet = () => api('/v1/wallet');
const importXml = (xml) => api('/v1/imports', { method: 'POST', body: { xml } });

async function* listAllInvoices(filters = {}) {
  let cursor;
  do {
    const qs = new URLSearchParams({ limit: '100', ...filters });
    if (cursor) qs.set('cursor', cursor);
    const page = await api(`/v1/invoices?${qs}`);
    yield* page.data;
    cursor = page.has_more ? page.next_cursor : null;
  } while (cursor);
}

// external_ref = chave de idempotência (obrigatório em produção — retries seguros)
async function emitInvoice({ companyId, externalRef, tomador, servico, valores, competencia, ...rest }) {
  return api('/v1/invoices', {
    method: 'POST',
    body: {
      company_id: companyId,
      external_ref: externalRef,
      competencia, // YYYY-MM-DD
      tomador,
      servico,
      valores,
      ...rest,
    },
  });
}

// ── Webhook: verificação de assinatura ──────────────────────────────────────
// Header "X-Hub-Signature: t=<unix>,v1=<hex>"; v1 = HMAC-SHA256(secret, "<t>.<corpo-cru>")
export function verifyWebhookSignature(secret, signatureHeader, rawBody, toleranceSec = 300) {
  const fields = {};
  for (const kv of String(signatureHeader).split(',')) {
    const i = kv.indexOf('=');
    if (i > 0) fields[kv.slice(0, i).trim()] = kv.slice(i + 1);
  }
  const t = Number(fields.t);
  const v1 = fields.v1;
  if (!t || !v1) return false;
  if (Math.abs(Date.now() / 1000 - t) > toleranceSec) return false; // anti-replay
  const expected = createHmac('sha256', secret).update(`${t}.${rawBody}`).digest('hex');
  const a = Buffer.from(expected, 'hex');
  const b = Buffer.from(v1, 'hex');
  return a.length === b.length && timingSafeEqual(a, b);
}

// ── Exemplo de uso ───────────────────────────────────────────────────────────
async function main() {
  const [company] = await listCompanies();
  if (!company) throw new Error('Nenhuma empresa na conta.');

  const { invoice, replayed } = await emitInvoice({
    companyId: company.id,
    externalRef: 'pedido-12345',
    competencia: '2026-10-23',
    tomador: {
      tipo_documento: 'CNPJ',
      documento: '11222333000144',
      nome: 'Cliente Exemplo LTDA',
      email: 'cliente@example.com',
    },
    servico: {
      codigo_tributacao_nacional: '010101',
      descricao: 'Serviço de desenvolvimento de software',
    },
    valores: { valor_servico: 1500.0, iss_retido: false },
  });
  console.log(replayed ? 'Nota já existia (replay):' : 'Nota emitida:', invoice.id, invoice.numero_nfse);

  const xmlUrl = await getFileUrl(invoice.id, 'xml');
  console.log('XML (60min):', xmlUrl);

  // await cancelInvoice(invoice.id, 'Serviço não prestado conforme contrato.');
  // await resendInvoice(invoice.id);
  // console.log(await getWallet());
}

// ── Receptor de webhook (node:http puro) ─────────────────────────────────────
// const server = createServer((req, res) => {
//   if (req.method !== 'POST' || req.url !== '/webhooks/hub') { res.writeHead(404); res.end(); return; }
//   const chunks = [];
//   req.on('data', (c) => chunks.push(c));
//   req.on('end', () => {
//     const rawBody = Buffer.concat(chunks).toString('utf8'); // corpo cru — nunca re-serialize
//     const sig = req.headers['x-hub-signature'];
//     if (!verifyWebhookSignature(process.env.HUB_WEBHOOK_SECRET, sig, rawBody)) {
//       res.writeHead(401); res.end(); return;
//     }
//     const event = JSON.parse(rawBody);
//     const deliveryId = req.headers['x-hub-delivery']; // deduplique por este id
//     console.log(event.event, deliveryId, event.data);
//     res.writeHead(200); res.end('ok'); // responda 2xx rápido; processe assíncrono
//   });
// });
// server.listen(3000);

main().catch((err) => {
  if (err instanceof HubApiError) console.error(`Erro da API ${err.code} (${err.status}) req=${err.requestId}`);
  else console.error(err);
  process.exitCode = 1;
});
