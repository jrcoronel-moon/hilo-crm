# Banner de arranque

Cada contenedor de Hilo (`web`, `mcp`, `migrate`) imprime al arrancar el logo en ASCII y lo necesario para saber
**qué** está corriendo:

```
          _
         (_)         _   _  ___  _       ___
    .~~~~| |~~.     | | | ||_ _|| |     / _ \
   (     | |   )    | |_| | | | | |    | | | |
    '~~~~| |~~'     |  _  | | | | |___ | |_| |
         | |        |_| |_||___||_____| \___/
         | |
         \ /        service   web  ·  listen :8080
          V         commit    9abf23e  ·  build 2026-10-29T15:24Z
                    migration 0001_init
                    enabled   google · microsoft · oauth 2.1 · lead forms
```

Después de un deploy, `docker logs hilo-web-1 2>&1 | head -15` dice en un segundo si el servidor tomó el código nuevo.

## Decisiones

- **Lo imprime el binario, no el build de Docker**: la salida de BuildKit no se ve y los pasos cacheados no imprimen.
- **Una vez, a stdout, sin prefijo del logger**, para que el dibujo no se desarme.
- **Colores ANSI apagables con `NO_COLOR`** ([no-color.org](https://no-color.org)); el override de producción lo
  activa para visores como CloudWatch.
- **El commit entra en el build** (`-ldflags -X main.gitSHA=…`): la imagen no contiene `.git`. Sin
  `GIT_SHA=$(git rev-parse --short HEAD)` el banner dice `dev`, que también es información.
- **La migración mostrada es la aplicada en la base**, no la más nueva del binario; si la base está atrasada el
  servicio no arranca.
- **Nunca muestra secretos**: solo nombres de integraciones activas.

Código: [`internal/banner`](../internal/banner/banner.go).
