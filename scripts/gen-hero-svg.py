#!/usr/bin/env python3
"""Generates the animated README diagram (tools -> agent -> Hilo) in English
(docs/img/hilo-agent.svg) and Spanish (docs/img/hilo-agente.svg). Pure SVG +
CSS, no script, so GitHub renders it as an image.
Run: python3 scripts/gen-hero-svg.py"""
from pathlib import Path

TEXT = {
    "en": dict(file="hilo-agent.svg", title="Hilo: the CRM an agent runs",
               desc="The tools you already use (inbox, Gmail, calendar, video calls, Drive, Docs, social, forms) "
                    "connect to the agent, and the agent records everything in Hilo over MCP.",
               surfaces=["Own inbox", "Gmail", "Calendar", "Meet · Zoom · Teams", "Drive", "Docs · Sheets",
                         "Social media", "Forms"],
               agent="Agent", client="MCP client", meta=["pipeline", "24 tools", "audited"],
               caption="The agent does the work with the tools you already have. Hilo is where it is recorded "
                       "and measured."),
    "es": dict(file="hilo-agente.svg", title="Hilo: el CRM que opera un agente",
               desc="Las herramientas (buzón, Gmail, calendario, videoconferencia, Drive, Docs, redes, formularios) "
                    "se conectan al agente, y el agente registra todo en Hilo por MCP.",
               surfaces=["Buzón propio", "Gmail", "Calendario", "Meet · Zoom · Teams", "Drive", "Docs · Sheets",
                         "Redes sociales", "Formularios"],
               agent="Agente", client="cliente MCP", meta=["pipeline", "24 tools", "auditado"],
               caption="El agente hace el trabajo con las herramientas que ya tenés. Hilo es donde queda "
                       "registrado y medido."),
}

W, H = 760, 320
MONO = 'ui-monospace,SFMono-Regular,"SF Mono",Menlo,Consolas,"Liberation Mono",monospace'
SANS = '-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"Helvetica Neue",Arial,sans-serif'
CW = 7.0  # monospace advance at 11.5px

AX, AY, AS = 330, 112, 84          # agent box (x, y, size)
ACX, ACY = AX + AS / 2, AY + AS / 2
CX, CY, CWD, CH = 492, 92, 240, 172  # Hilo card

def render(t):
    out = []
    add = out.append
    add(f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" width="{W}" height="{H}" role="img" '
        'aria-labelledby="t d">')
    add(f'<title id="t">{t["title"]}</title>')
    add(f'<desc id="d">{t["desc"]}</desc>')
    add(f'''<style>
    .bg{{fill:#fafbf9}} .card{{fill:#fff;stroke:#d7e8dc}} .dot{{fill:#dfe7e1}}
    .pill{{fill:#fff;stroke:#e3e9e1}} .pd{{fill:#5aa07c}}
    .pt{{font:500 11.5px {MONO};fill:#54615a}}
    .wire{{fill:none;stroke:#bcd6c6;stroke-width:1.5;stroke-linecap:round}}
    .flow{{fill:none;stroke:#5aa07c;stroke-width:2.2;stroke-linecap:round;stroke-dasharray:14 100;stroke-dashoffset:0;
      animation:dash 2.8s linear infinite}}
    .rev{{stroke:#77bf9b;stroke-width:2}}
    @keyframes dash{{to{{stroke-dashoffset:-114}}}}
    .orb{{animation:float 5.2s ease-in-out infinite}}
    @keyframes float{{0%,100%{{transform:translateY(0)}}50%{{transform:translateY(-6px)}}}}
    .ring{{fill:none;stroke:#5aa07c;stroke-width:2;opacity:0;transform-box:fill-box;transform-origin:center;
      animation:pulse 3.4s ease-out infinite}}
    @keyframes pulse{{0%{{transform:scale(1);opacity:.5}}70%{{transform:scale(1.34);opacity:0}}100%{{opacity:0}}}}
    .blink{{animation:blink 2.6s ease-in-out infinite}}
    @keyframes blink{{0%,100%{{opacity:1}}50%{{opacity:.35}}}}
    .bar{{fill:#d7e8dc}} .fill{{fill:url(#g);transform-box:fill-box;transform-origin:left;animation:grow 3.6s ease-in-out infinite}}
    @keyframes grow{{0%,100%{{transform:scaleX(1)}}50%{{transform:scaleX(.72)}}}}
    .h{{font:600 14px {SANS};fill:#16201b}} .s{{font:500 11px {MONO};fill:#8b968f}}
    .badge{{fill:#e9f3ec}} .bt{{font:700 9.5px {MONO};fill:#3d7a5c;letter-spacing:.06em}}
    .cap{{font:400 12.5px {SANS};fill:#8b968f}}
    @media (prefers-reduced-motion:reduce){{.flow,.orb,.ring,.blink,.fill{{animation:none}}.flow{{display:none}}}}
    </style>''')
    add('<defs><linearGradient id="g" x1="0" x2="1"><stop offset="0" stop-color="#5aa07c"/>'
        '<stop offset="1" stop-color="#3d7a5c"/></linearGradient>'
        '<linearGradient id="og" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#fff"/>'
        '<stop offset="1" stop-color="#e8f3ec"/></linearGradient>'
        '<filter id="sh" x="-20%" y="-20%" width="140%" height="160%"><feDropShadow dx="0" dy="8" stdDeviation="10" '
        'flood-color="#16281e" flood-opacity=".09"/></filter></defs>')
    add(f'<rect class="bg" x="1" y="1" width="{W-2}" height="{H-2}" rx="22" stroke="#e3e9e1"/>')
    for gx in range(30, W - 20, 22):           # dotted grid
        for gy in range(24, H - 60, 22):
            add(f'<circle class="dot" cx="{gx}" cy="{gy}" r="1"/>')

    wires, flows = [], []
    top, step = 34, 31
    for i, name in enumerate(t["surfaces"]):
        y = top + i * step
        w = 26 + len(name) * CW
        add(f'<rect class="pill" x="24" y="{y}" width="{w:.0f}" height="23" rx="11.5"/>'
            f'<circle class="pd" cx="36" cy="{y+11.5}" r="2.6"/>'
            f'<text class="pt" x="45" y="{y+15.5}">{name}</text>')
        x0, y0 = 24 + w + 2, y + 11.5
        x3, y3 = AX - 2, ACY
        d = f"M{x0:.0f} {y0:.1f} C{x0+90:.0f} {y0:.1f} {x3-90:.0f} {y3:.1f} {x3:.0f} {y3:.1f}"
        r = f"M{x3:.0f} {y3:.1f} C{x3-90:.0f} {y3:.1f} {x0+90:.0f} {y0:.1f} {x0:.0f} {y0:.1f}"
        wires.append(d)
        flows.append((d, "", i * 0.35))
        flows.append((r, " rev", i * 0.35 + 1.4))
    d = f"M{AX+AS+2} {ACY} C{AX+AS+40} {ACY} {CX-40} {CY+CH/2} {CX-2} {CY+CH/2}"
    r = f"M{CX-2} {CY+CH/2} C{CX-40} {CY+CH/2} {AX+AS+40} {ACY} {AX+AS+2} {ACY}"
    wires.append(d)
    flows += [(d, "", 0.2), (r, " rev", 1.6)]
    for d in wires:
        add(f'<path class="wire" d="{d}"/>')
    for d, cls, delay in flows:
        add(f'<path class="flow{cls}" d="{d}" pathLength="100" style="animation-delay:{delay:.2f}s"/>')

    # agent
    add(f'<g class="orb"><rect class="ring" x="{AX}" y="{AY}" width="{AS}" height="{AS}" rx="25"/>'
        f'<rect x="{AX}" y="{AY}" width="{AS}" height="{AS}" rx="25" fill="url(#og)" stroke="#d7e8dc" filter="url(#sh)"/>'
        f'<g transform="translate({ACX-20} {ACY-20}) scale(1.25)" fill="none" stroke="#3d7a5c" stroke-width="1.7" '
        'stroke-linecap="round" stroke-linejoin="round"><rect x="5" y="8" width="22" height="17" rx="5"/>'
        '<path d="M16 8V4"/><circle cx="16" cy="3" r="1.6" fill="#3d7a5c" stroke="none"/>'
        '<circle cx="11.5" cy="16" r="2.1" fill="#3d7a5c" stroke="none"/>'
        '<circle cx="20.5" cy="16" r="2.1" fill="#3d7a5c" stroke="none"/><path d="M12.5 21.5h7"/></g></g>')
    add(f'<text class="h" x="{ACX}" y="{AY+AS+28}" text-anchor="middle">{t["agent"]}</text>'
        f'<text class="s" x="{ACX}" y="{AY+AS+48}" text-anchor="middle">{t["client"]}</text>')

    # Hilo card
    add(f'<rect class="card" x="{CX}" y="{CY}" width="{CWD}" height="{CH}" rx="18" filter="url(#sh)"/>')
    add(f'<circle class="pd blink" cx="{CX+22}" cy="{CY+30}" r="4.5"/>'
        f'<text class="h" x="{CX+34}" y="{CY+35}">Hilo Agentic CRM</text>'
        f'<rect class="badge" x="{CX+CWD-50}" y="{CY+21}" width="32" height="18" rx="5"/>'
        f'<text class="bt" x="{CX+CWD-34}" y="{CY+33.5}" text-anchor="middle">MCP</text>'
        f'<path d="M{CX+18} {CY+54}H{CX+CWD-18}" stroke="#e3e9e1"/>')
    bw = CWD - 36
    for i, pct in enumerate([.82, .58, .70, .41]):
        y = CY + 70 + i * 13
        add(f'<rect class="bar" x="{CX+18}" y="{y}" width="{bw}" height="7" rx="3.5"/>'
            f'<rect class="fill" x="{CX+18}" y="{y}" width="{bw*pct:.0f}" height="7" rx="3.5" '
            f'style="animation-delay:{i*0.3:.1f}s"/>')
    add(f'<path d="M{CX+18} {CY+132}H{CX+CWD-18}" stroke="#e3e9e1"/>')
    for x, label, a in zip([CX+18, CX+CWD/2, CX+CWD-18], t["meta"], ["start", "middle", "end"]):
        add(f'<text class="s" x="{x:.0f}" y="{CY+153}" text-anchor="{a}">{label}</text>')

    add(f'<text class="cap" x="{W/2}" y="{H-24}" text-anchor="middle">{t["caption"]}</text>')
    add('</svg>')
    Path(__file__).resolve().parent.parent.joinpath("docs/img", t["file"]).write_text("\n".join(out) + "\n")


for lang in TEXT:
    render(TEXT[lang])
