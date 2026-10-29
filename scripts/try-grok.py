#!/usr/bin/env python3
"""Connect Grok (xAI API) to a local Hilo CRM and run one prompt, step by step.

    ./scripts/try-grok.py ["your prompt"]

It asks for (or reads from the environment) XAI_API_KEY and HILO_PAT, never
prints them, and checks each link of the chain so the failing one is obvious:

  1. the xAI key works and a Grok model is available;
  2. the Hilo token is valid against the local stack;
  3. a public tunnel reaches Hilo (starts `cloudflared` for you unless
     HILO_MCP_URL is set) — xAI's servers call /mcp, not your laptop;
  4. Grok runs the prompt with Hilo as a remote MCP tool.

Optional: XAI_MODEL, HILO_LOCAL_URL (default http://localhost:<HILO_HTTP_PORT>).
Only the Python standard library is used.
"""

import getpass
import json
import os
import re
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.request

XAI = "https://api.x.ai/v1"
PAT_RE = re.compile(r"^hilo_pat_[A-Za-z0-9_-]{43}$")
DEFAULT_PROMPT = (
    "Usá las herramientas de Hilo: listá los pipelines y creá (o actualizá) una oportunidad "
    "\"Demo Grok\" de 1000 USD para la empresa acme.com. Respondé con un resumen corto."
)


def ok(msg):
    print(f"  ✓ {msg}")


def fail(msg, hint=""):
    print(f"  ✗ {msg}")
    if hint:
        print(f"    → {hint}")
    sys.exit(1)


def http(method, url, headers=None, body=None, timeout=20):
    """Returns (status, parsed JSON or text). Never raises on HTTP errors."""
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method, headers=headers or {})
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            raw = r.read().decode()
            status = r.status
    except urllib.error.HTTPError as e:
        raw, status = e.read().decode(errors="replace"), e.code
    except (urllib.error.URLError, TimeoutError) as e:
        return 0, str(e)
    try:
        return status, json.loads(raw)
    except ValueError:
        return status, raw


def secret(env, prompt, clean=lambda s: s):
    v = os.environ.get(env) or getpass.getpass(f"{prompt}: ")
    return clean(v)


def clean_pat(v):
    # Copying from a terminal often drags along zsh's "%", quotes or spaces.
    v = v.strip().strip("'\"").removeprefix("Bearer ").strip().rstrip("%").strip()
    return v


def local_url():
    if os.environ.get("HILO_LOCAL_URL"):
        return os.environ["HILO_LOCAL_URL"].rstrip("/")
    port = "8080"
    env = os.path.join(os.path.dirname(__file__), "..", ".env")
    if os.path.exists(env):
        for line in open(env):
            if line.startswith("HILO_HTTP_PORT="):
                port = line.split("=", 1)[1].strip() or port
    return f"http://localhost:{port}"


MCP_INIT = {
    "jsonrpc": "2.0", "id": 1, "method": "initialize",
    "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "try-grok", "version": "1"}},
}


def mcp_initialize(url, pat):
    return http("POST", url, {"Authorization": f"Bearer {pat}", "Accept": "application/json, text/event-stream"}, MCP_INIT)[0]


def start_tunnel(target):
    exe = shutil.which("cloudflared")
    if not exe:
        fail("cloudflared no está instalado", "brew install cloudflared   (o definí HILO_MCP_URL con tu propio túnel)")
    proc = subprocess.Popen([exe, "tunnel", "--no-autoupdate", "--url", target],
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    deadline = time.time() + 40
    while time.time() < deadline:
        line = proc.stdout.readline()
        m = re.search(r"https://[a-z0-9-]+\.trycloudflare\.com", line or "")
        if m:
            return proc, m.group(0)
    proc.terminate()
    fail("cloudflared no devolvió una URL a tiempo", "probá de nuevo; las URLs rápidas a veces tardan")
    return None, ""


def pick_model(models):
    ids = [m.get("id", "") for m in models]
    if os.environ.get("XAI_MODEL"):
        return os.environ["XAI_MODEL"]
    for want in ("grok-4", "grok-3"):
        for i in ids:
            if i.startswith(want) and "image" not in i and "vision" not in i:
                return i
    return ids[0] if ids else ""


def run_grok(key, model, mcp_url, authorization, prompt):
    body = {"model": model, "input": prompt,
            "tools": [{"type": "mcp", "server_label": "hilo", "server_url": mcp_url, "authorization": authorization}]}
    return http("POST", f"{XAI}/responses", {"Authorization": f"Bearer {key}"}, body, timeout=240)


def summarize(resp):
    calls, texts, errors = [], [], []
    for item in resp.get("output", []) if isinstance(resp, dict) else []:
        t = item.get("type", "")
        if t == "mcp_call":
            calls.append(item.get("name", "?"))
            if item.get("error"):
                errors.append(str(item["error"])[:200])
        elif t == "mcp_list_tools" and item.get("error"):
            errors.append(str(item["error"])[:200])
        elif t == "message":
            for c in item.get("content", []):
                if c.get("type") in ("output_text", "text"):
                    texts.append(c.get("text", ""))
    return calls, texts, errors


def main():
    prompt = sys.argv[1] if len(sys.argv) > 1 else DEFAULT_PROMPT
    local = local_url()
    print("Hilo × Grok\n")

    print("1. Key de xAI")
    key = secret("XAI_API_KEY", "API key de xAI (no se muestra)", lambda s: s.strip())
    status, models = http("GET", f"{XAI}/models", {"Authorization": f"Bearer {key}"})
    if not key:
        fail("la key está vacía", "pegala cuando el script la pida, o: export XAI_API_KEY='xai-...'")
    if status in (401, 403) or (status == 400 and "api key" in str(models).lower()):
        fail("xAI rechaza la key", "creá una nueva en console.x.ai → API Keys y copiala completa, sin espacios")
    if status != 200 or not isinstance(models, dict):
        fail(f"no pude hablar con xAI (HTTP {status})", "revisá tu conexión o el estado de api.x.ai")
    model = pick_model(models.get("data", []))
    if not model:
        fail("tu cuenta no tiene modelos disponibles", "revisá el crédito o los permisos del equipo en console.x.ai")
    ok(f"key válida · modelo {model}")

    print("2. Token de Hilo")
    pat = secret("HILO_PAT", "Token de Hilo hilo_pat_… (no se muestra)", clean_pat)
    if not PAT_RE.match(pat):
        fail("el token no tiene el formato hilo_pat_ + 43 caracteres",
             "copialo de nuevo desde Ajustes → Agentes (se muestra una sola vez; si no lo tenés, creá otro)")
    st = mcp_initialize(f"{local}/mcp", pat)
    if st == 0:
        fail(f"Hilo no responde en {local}", "levantá el stack con: make up")
    if st == 401:
        fail("Hilo rechaza el token", "puede estar revocado o vencido; el motivo exacto: docker logs hilo-mcp-1 | grep 'auth rejected'")
    if st != 200:
        fail(f"respuesta inesperada de Hilo: HTTP {st}")
    ok(f"token válido contra {local}")

    print("3. Túnel público")
    proc = None
    public = os.environ.get("HILO_MCP_URL", "").rstrip("/")
    if public:
        public = public.removesuffix("/mcp")
    else:
        proc, public = start_tunnel(local)
    try:
        st = 0
        for _ in range(15):  # DNS of a fresh quick tunnel takes a few seconds
            st = mcp_initialize(f"{public}/mcp", pat)
            if st == 200:
                break
            time.sleep(2)
        if st != 200:
            fail(f"el túnel {public} no llega a Hilo (HTTP {st})", "si lo abriste vos, apuntalo a " + local)
        ok(f"{public}/mcp responde")

        print("4. Grok con Hilo como herramienta MCP (puede tardar un minuto)")
        for authorization in (pat, f"Bearer {pat}"):  # xAI's docs do not say whether it adds "Bearer"
            status, resp = run_grok(key, model, f"{public}/mcp", authorization, prompt)
            if status != 200:
                msg = resp.get("error") if isinstance(resp, dict) else str(resp)[:300]
                fail(f"xAI devolvió HTTP {status}: {msg}")
            calls, texts, errors = summarize(resp)
            if calls and not errors:
                ok("Grok usó: " + ", ".join(calls))
                print("\nRespuesta de Grok:\n")
                print("\n".join(texts).strip() or json.dumps(resp, indent=2)[:3000])
                print("\nMirá el resultado en " + local + "/board  (Ajustes → Auditoría muestra cada acción del token)")
                return
        detail = "; ".join(errors) or "Grok no llamó a ninguna herramienta"
        fail(f"Grok no pudo usar Hilo: {detail}", "motivo del lado de Hilo: docker logs hilo-mcp-1 | grep 'auth rejected'")
    finally:
        if proc:
            proc.terminate()
            print("\n(túnel cerrado)")


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        print("\ncancelado")
        sys.exit(130)
