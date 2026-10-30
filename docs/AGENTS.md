# Conectar tu agente

Hilo es un servidor MCP remoto (Streamable HTTP) en `https://<tu-dominio>/mcp`. Cualquier cliente MCP se conecta de
una de dos formas:

- **OAuth** (recomendado): pegás la URL, el cliente descubre todo solo, te abre Hilo para iniciar sesión, elegís el
  workspace y autorizás. Hilo soporta registro dinámico de clientes (DCR) y Client ID Metadata Documents (CIMD).
- **Token personal (PAT)**: para clientes que solo aceptan un encabezado. Se crea en *Ajustes → Agentes* o con
  `hiloctl token create <workspace> <email> <nombre> [--read-only] [--ttl 90d]`, y se manda como
  `Authorization: Bearer hilo_pat_…`.

## Compatibilidad

"Probado" quiere decir que lo conectamos de punta a punta contra Hilo. "Por verificar" quiere decir que, según su
documentación, debería funcionar, pero no lo probamos.

| Cliente | Cómo se conecta | Estado |
|---|---|---|
| Claude (web, escritorio) | Conector personalizado con la URL, OAuth | Probado |
| Claude Code | `claude mcp add --transport http hilo <URL>`, OAuth | Probado |
| Grok (API de xAI) | Herramienta `mcp` en `/v1/responses` con un PAT | Probado (`scripts/try-grok.py`) |
| Cursor | Servidor MCP remoto, OAuth o PAT | Por verificar |
| ChatGPT y ChatGPT Dots | Conector MCP remoto, OAuth | Por verificar |
| Claude Duet | Usa los conectores de Claude: conectá Hilo en Claude | Por verificar (vista previa con lista de espera) |
| Meta Muse | Conector personalizado; las fuentes no coinciden en si acepta OAuth o solo encabezados | Por verificar: si solo acepta encabezados, usá un PAT |

Si conectás un cliente de la lista "por verificar", contanos cómo te fue y actualizamos la tabla.

## Para agentes que trabajan solos (Duet, Muse, Dots…)

Los agentes proactivos revisan tus apps cada tanto y te piden confirmación antes de hacer algo delicado. Para eso
Hilo ofrece:

- **`list_changes`**: qué cambió desde la última vez que miró. El agente guarda `next_cursor` y lo pasa en la
  próxima llamada, así nunca se pierde ni repite un cambio.
- **`needs_attention`**: oportunidades abiertas que necesitan a alguien, con el motivo: próximo paso vencido, fecha
  de cierre pasada, sin actividad, sin dueño o sin próximo paso. Ideal para un resumen diario.
- **Anotaciones MCP**: las herramientas de lectura se declaran de solo lectura, y las que modifican o archivan
  (`upsert_*`, `move_opportunity_stage`, `archive_record`, `bulk_upsert`) se declaran destructivas, para que el
  agente pida tu confirmación según su política.
- **Conexión duradera**: el agente renueva su acceso solo (refresh tokens rotados). Lo podés desconectar en
  cualquier momento desde *Ajustes → Agentes*.

## Conectar sin pantallas

Un admin puede pre-aprobar un agente para un workspace. Así ese agente se conecta sin la pantalla de
consentimiento, y si Hilo tiene un único proveedor de login configurado, sin ninguna pantalla de Hilo:

```sh
docker compose exec web hiloctl client preapprove <workspace> <client_id del agente>
```

El `client_id` aparece en *Ajustes → Agentes* después de conectar el agente una vez. Solo se pueden pre-aprobar
agentes identificados por URL (CIMD), con lectura y escritura como máximo: la administración siempre pide
consentimiento.
