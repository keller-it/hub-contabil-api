<?php
// Hub Contábil — API /v1 em PHP 8+ (cURL nativo, sem dependências).
// Uso:   export HUB_API_KEY=hk_test_…   (ou hk_live_…)
//        php hub-api.php

const BASE_URL = 'https://api.hub-contabil.com';
$API_KEY = getenv('HUB_API_KEY') ?: throw new RuntimeException('Defina HUB_API_KEY=hk_test_...');

class HubApiException extends RuntimeException {
    public function __construct(
        public readonly int $httpStatus,
        public readonly string $apiCode,
        string $message,
        public readonly ?string $requestId = null,
    ) {
        parent::__construct("{$httpStatus} {$apiCode}: {$message}");
    }
}

// Envelope: sucesso → array; falha → HubApiException(httpStatus, apiCode, requestId)
function hubApi(string $path, string $method = 'GET', ?array $body = null): array {
    $ch = curl_init(BASE_URL . $path);
    curl_setopt_array($ch, [
        CURLOPT_CUSTOMREQUEST => $method,
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_TIMEOUT => 30,
        CURLOPT_HTTPHEADER => array_filter([
            'Authorization: Bearer ' . $GLOBALS['API_KEY'],
            $body !== null ? 'Content-Type: application/json' : null,
        ]),
        CURLOPT_POSTFIELDS => $body !== null ? json_encode($body) : null,
    ]);
    $raw = curl_exec($ch);
    if ($raw === false) throw new RuntimeException('cURL: ' . curl_error($ch));
    $status = curl_getinfo($ch, CURLINFO_RESPONSE_CODE);
    curl_close($ch);
    $data = json_decode($raw, true);
    if ($status >= 400) {
        $err = $data['error'] ?? [];
        throw new HubApiException($status, $err['code'] ?? 'UNKNOWN', $err['message'] ?? $raw, $err['request_id'] ?? null);
    }
    return $data ?? [];
}

function listCompanies(): array { return hubApi('/v1/companies')['data']; }
function getInvoice(string $id): array { return hubApi("/v1/invoices/{$id}")['invoice']; }
function getFileUrl(string $id, string $type = 'xml'): string { return hubApi("/v1/invoices/{$id}/files?type={$type}")['url']; }
function getWallet(): array { return hubApi('/v1/wallet'); }
function importXml(string $xml): array { return hubApi('/v1/imports', 'POST', ['xml' => $xml]); }
function resendInvoice(string $id, ?string $email = null): array {
    return hubApi("/v1/invoices/{$id}/resend", 'POST', $email ? ['email' => $email] : []);
}
function cancelInvoice(string $id, string $motivo, string $codigoMotivo = '2'): array {
    // motivo: 15–255 caracteres; codigo 1=erro na emissão, 2=serviço não prestado, 9=outros
    return hubApi("/v1/invoices/{$id}/cancel", 'POST', ['codigo_motivo' => $codigoMotivo, 'motivo' => $motivo]);
}

function listAllInvoices(array $filters = []): Generator {
    $cursor = null;
    do {
        $qs = http_build_query(['limit' => 100] + $filters + ($cursor ? ['cursor' => $cursor] : []));
        $page = hubApi("/v1/invoices?{$qs}");
        yield from $page['data'];
        $cursor = $page['has_more'] ? $page['next_cursor'] : null;
    } while ($cursor);
}

// external_ref = chave de idempotência (obrigatório em produção — retries seguros)
function emitInvoice(array $payload): array {
    return hubApi('/v1/invoices', 'POST', $payload);
}

// ── Webhook: verificação de assinatura ─────────────────────────────────────
// Header "X-Hub-Signature: t=<unix>,v1=<hex>"; v1 = HMAC-SHA256(secret, "<t>.<corpo-cru>")
function verifyWebhookSignature(string $secret, string $signatureHeader, string $rawBody, int $toleranceSec = 300): bool {
    $fields = [];
    foreach (explode(',', $signatureHeader) as $kv) {
        $i = strpos($kv, '=');
        if ($i !== false) $fields[trim(substr($kv, 0, $i))] = substr($kv, $i + 1);
    }
    $t = isset($fields['t']) ? (int) $fields['t'] : 0;
    $v1 = $fields['v1'] ?? '';
    if (!$t || $v1 === '') return false;
    if (abs(time() - $t) > $toleranceSec) return false; // anti-replay
    $expected = hash_hmac('sha256', "{$t}.{$rawBody}", $secret);
    return hash_equals($expected, $v1);
}

// ── Receptor de webhook (PHP puro / SAPI) ──────────────────────────────────
// $rawBody = file_get_contents('php://input'); // corpo cru — nunca re-serialize
// $sig = $_SERVER['HTTP_X_HUB_SIGNATURE'] ?? '';
// if (!verifyWebhookSignature(getenv('HUB_WEBHOOK_SECRET'), $sig, $rawBody)) { http_response_code(401); exit; }
// $event = json_decode($rawBody, true);
// $deliveryId = $_SERVER['HTTP_X_HUB_DELIVERY'] ?? null; // deduplique por este id
// // processe $event['event'] / $event['data'] assíncrono e responda 200:
// http_response_code(200); echo 'ok';

// ── Exemplo de uso ─────────────────────────────────────────────────────────
$companies = listCompanies();
if (!$companies) exit("Nenhuma empresa na conta.\n");
$company = $companies[0];

$result = emitInvoice([
    'company_id' => $company['id'],
    'external_ref' => 'pedido-12345',
    'competencia' => '2026-10-23',
    'tomador' => [
        'tipo_documento' => 'CNPJ',
        'documento' => '11222333000144',
        'nome' => 'Cliente Exemplo LTDA',
        'email' => 'cliente@example.com',
    ],
    'servico' => [
        'codigo_tributacao_nacional' => '010101',
        'descricao' => 'Serviço de desenvolvimento de software',
    ],
    'valores' => ['valor_servico' => 1500.00, 'iss_retido' => false],
]);
$invoice = $result['invoice'];
echo ($result['replayed'] ? 'replay: ' : 'emitida: '), $invoice['id'], ' ', $invoice['numero_nfse'] ?? '-', "\n";
echo 'XML (60min): ', getFileUrl($invoice['id'], 'xml'), "\n";
// cancelInvoice($invoice['id'], 'Serviço não prestado conforme contrato.');
// print_r(getWallet());
