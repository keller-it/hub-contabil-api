# Hub Contábil — API /v1 em Python 3.
# Dep.:  pip install requests   (flask só para o receptor de webhook no final)
# Uso:   export HUB_API_KEY=hk_test_…   (ou hk_live_…)
#        python hub_api.py

import hashlib
import hmac
import os
import time

import requests

BASE_URL = os.environ.get("HUB_API_URL", "https://api.hub-contabil.com")
API_KEY = os.environ["HUB_API_KEY"]  # hk_test_… ou hk_live_…


class HubApiError(Exception):
    def __init__(self, status: int, code: str, message: str, request_id: str | None = None):
        super().__init__(f"{status} {code}: {message}")
        self.status, self.code, self.request_id = status, code, request_id


# Envelope: sucesso → JSON; falha → HubApiError(status, code, request_id)
def api(path: str, method: str = "GET", body: dict | None = None):
    resp = requests.request(
        method,
        f"{BASE_URL}{path}",
        headers={"Authorization": f"Bearer {API_KEY}"},
        json=body,
        timeout=30,
    )
    try:
        data = resp.json()
    except ValueError:
        data = None
    if not resp.ok:
        err = (data or {}).get("error") or {}
        raise HubApiError(resp.status_code, err.get("code", "UNKNOWN"), err.get("message", resp.text), err.get("request_id"))
    return data


def list_companies():
    return api("/v1/companies")["data"]


def emit_invoice(company_id: str, external_ref: str, tomador: dict, servico: dict, valores: dict, competencia: str, **extra):
    # external_ref = chave de idempotência (obrigatório em produção — retries seguros)
    return api("/v1/invoices", "POST", {
        "company_id": company_id,
        "external_ref": external_ref,
        "competencia": competencia,  # YYYY-MM-DD
        "tomador": tomador,
        "servico": servico,
        "valores": valores,
        **extra,
    })


def get_invoice(invoice_id: str):
    return api(f"/v1/invoices/{invoice_id}")["invoice"]


def list_invoices(**filters):
    """Itera todas as páginas (cursor created_at|id) até has_more=false."""
    cursor = None
    while True:
        params = {"limit": 100, **filters}
        if cursor:
            params["cursor"] = cursor
        qs = "&".join(f"{k}={requests.utils.quote(str(v))}" for k, v in params.items())
        page = api(f"/v1/invoices?{qs}")
        yield from page["data"]
        if not page["has_more"]:
            return
        cursor = page["next_cursor"]


def get_file_url(invoice_id: str, kind: str = "xml") -> str:
    return api(f"/v1/invoices/{invoice_id}/files?type={kind}")["url"]


def cancel_invoice(invoice_id: str, motivo: str, codigo_motivo: str = "2"):
    # motivo: 15–255 caracteres; codigo 1=erro na emissão, 2=serviço não prestado, 9=outros
    return api(f"/v1/invoices/{invoice_id}/cancel", "POST", {"codigo_motivo": codigo_motivo, "motivo": motivo})


def resend_invoice(invoice_id: str, email: str | None = None):
    return api(f"/v1/invoices/{invoice_id}/resend", "POST", {"email": email} if email else {})


def import_xml(xml: str):
    return api("/v1/imports", "POST", {"xml": xml})


def get_wallet():
    return api("/v1/wallet")


# ── Webhook: verificação de assinatura ─────────────────────────────────────
# Header "X-Hub-Signature: t=<unix>,v1=<hex>"; v1 = HMAC-SHA256(secret, "<t>.<corpo-cru>")
def verify_webhook_signature(secret: str, signature_header: str, raw_body: bytes, tolerance_sec: int = 300) -> bool:
    fields = {}
    for kv in (signature_header or "").split(","):
        if "=" in kv:
            k, v = kv.split("=", 1)
            fields[k.strip()] = v
    t, v1 = fields.get("t"), fields.get("v1")
    if not t or not v1:
        return False
    try:
        if abs(time.time() - int(t)) > tolerance_sec:  # anti-replay
            return False
    except ValueError:
        return False
    expected = hmac.new(secret.encode(), f"{t}.".encode() + raw_body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, v1)


# ── Exemplo de uso ─────────────────────────────────────────────────────────
def main():
    companies = list_companies()
    if not companies:
        raise SystemExit("Nenhuma empresa na conta.")
    company = companies[0]

    result = emit_invoice(
        company_id=company["id"],
        external_ref="pedido-12345",
        competencia="2026-10-23",
        tomador={
            "tipo_documento": "CNPJ",
            "documento": "11222333000144",
            "nome": "Cliente Exemplo LTDA",
            "email": "cliente@example.com",
        },
        servico={
            "codigo_tributacao_nacional": "010101",
            "descricao": "Serviço de desenvolvimento de software",
        },
        valores={"valor_servico": 1500.00, "iss_retido": False},
    )
    invoice = result["invoice"]
    print("replay:" if result["replayed"] else "emitida:", invoice["id"], invoice.get("numero_nfse"))
    print("XML (60min):", get_file_url(invoice["id"], "xml"))
    # cancel_invoice(invoice["id"], "Serviço não prestado conforme contrato.")
    # print(get_wallet())


if __name__ == "__main__":
    main()


# ── Receptor de webhook (Flask) ────────────────────────────────────────────
# pip install flask
# from flask import Flask, abort, request
#
# app = Flask(__name__)
#
# @app.post("/webhooks/hub")
# def webhook():
#     raw = request.get_data()  # corpo cru — nunca re-serialize
#     sig = request.headers.get("X-Hub-Signature", "")
#     if not verify_webhook_signature(os.environ["HUB_WEBHOOK_SECRET"], sig, raw):
#         abort(401)
#     event = request.get_json(force=True)
#     delivery_id = request.headers.get("X-Hub-Delivery")  # deduplique por este id
#     print(event["event"], delivery_id, event["data"])
#     return "ok"  # responda 2xx rápido; processe assíncrono
