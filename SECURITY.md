# Seguridad

## Reportar una vulnerabilidad

No abras un issue público. Usá **GitHub → Security → Report a vulnerability** (aviso privado) en este repositorio.
Respondemos dentro de las 72 h y coordinamos la publicación una vez disponible el arreglo.

## Modelo de amenazas (resumen)

| Activo | Amenaza | Defensa |
|---|---|---|
| Datos de un workspace | Otro tenant los lee o modifica | FKs compuestas `(workspace_id, id)` en todas las relaciones; Row-Level Security forzada en todas las tablas del CRM, ligada por transacción (`set_config(..., true)`); filtro explícito por workspace en cada consulta; tests de aislamiento sobre servicio, MCP y web. |
| Base de datos | SQL injection | Solo SQL estático generado por sqlc con parámetros; los patrones `LIKE` escapan `%` y `_`. El rol de la app no tiene DDL ni puede borrar el audit log. |
| Cuentas | Login con email no verificado, nOAuth (Microsoft) | Google: `email_verified`. Microsoft: identidad `tid:oid` y el email solo cuenta si Microsoft verifica el dominio (`xms_edov`) o es una cuenta personal. `state`, `nonce` y PKCE también hacia el proveedor; validación de `iss`, `aud`, `exp`. |
| Acceso a la plataforma | Cualquiera se registra | Cerrado por defecto: dominio habilitado por un superadmin o invitación. |
| Agentes (OAuth) | Robo de código o token, cliente impostor | PKCE S256 obligatorio; `redirect_uri` exacto (solo el puerto se relaja en loopback, RFC 8252); códigos de un solo uso de 2 min cuya repetición revoca la familia; refresh tokens rotados con detección de reuso (RFC 9700); audiencia ligada a `/mcp` (RFC 8707); pantalla de consentimiento que muestra si el cliente está verificado (CIMD) y a qué host vuelve el código; revocación desde Ajustes y RFC 7009. La pantalla de consentimiento solo se saltea cuando un admin pre-aprobó al agente (`hiloctl client preapprove`): únicamente clientes identificados por URL (CIMD), nunca con `crm:admin`, y solo si un único workspace del usuario lo confía. |
| Servidor (SSRF) | Un `client_id` CIMD apunta a la red interna | Solo `https` en el puerto 443; el dialer rechaza, ya resuelto el DNS, loopback, redes privadas, link-local (metadata de la nube), CGNAT, NAT64 y rangos reservados; sin redirecciones; límite de 64 KiB y 5 s. |
| Sesiones web | CSRF, robo de cookie, clickjacking | Cookie `__Host-` HttpOnly, SameSite=Lax y Secure; token CSRF por sesión y verificación de `Origin`; `frame-ancestors 'none'`. |
| Navegador | XSS | `html/template` (escape por contexto); CSP sin `unsafe-inline` ni orígenes externos; URLs de usuario solo `http(s)`. |
| Agentes | Prompt injection desde datos del CRM | Las instrucciones del servidor y las descripciones de herramientas declaran que todo texto del CRM es dato, no instrucción. Los formularios públicos no pueden sobrescribir registros existentes. |
| Tiempo real (`/events`) | Ver cambios de otro workspace, agotar el servidor | Requiere sesión y solo entrega eventos del workspace activo; el aviso de la base lleva únicamente referencias y el evento se lee con el workspace ligado (RLS). Límite de conexiones por usuario y total; un cliente lento pierde eventos en lugar de frenar al resto. |
| Formularios públicos | Spam, abuso | Clave no adivinable, rate limit por IP, honeypot, lista de orígenes, límite de cuerpo, redirección solo a la URL configurada. |
| Secretos | Fuga en el repo o en imágenes | Solo se guardan hashes de tokens; `.gitignore` y `.dockerignore` restrictivos; gitleaks en pre-commit y CI; imágenes distroless sin shell. |
| Infraestructura | Escalada desde un contenedor | Contenedores sin root, sistema de archivos de solo lectura, `cap_drop: ALL`, `no-new-privileges`, límites de memoria; Postgres en una red interna sin puertos publicados. |
| Disponibilidad | Abuso de API | Rate limit por IP (registro, token, login, formularios) y por token (MCP); timeouts estrictos del servidor HTTP. |

## Lo que la instancia necesita de quien la opera

- HTTPS (Caddy lo provee si el DNS apunta a la instancia y los puertos 80/443 están abiertos).
- Secretos largos y aleatorios en `.env`, con permisos `600`, fuera del repositorio.
- Backups fuera del host (snapshots de Lightsail o copia del volumen `backups`).
- Actualizar con regularidad (`git pull && make` y el despliegue descrito en `docs/DEPLOY_LIGHTSAIL.md`).
- Tratar el acceso SSH al servidor como acceso total: `hiloctl` actúa como propietario de cualquier workspace (emite
  tokens, cambia roles, exporta todo). `hiloctl export` produce el workspace completo en claro: guardalo como un
  backup.
- Si nadie necesita la web, `HILO_UI=minimal` (solo login y consentimiento de agentes) o `HILO_UI=off` (solo tokens
  personales) reducen la superficie expuesta.
