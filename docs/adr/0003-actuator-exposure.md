# The Actuator shares the application's port, and a whitelist decides what exists

The Actuator mounts on the server the application already runs, under an `/actuator` prefix, the
way Spring Boot does when `management.server.port` is unset. That puts operational endpoints on the
same public Ingress as application traffic. Rather than guard them, go-boot does not build them:
**`actuator.expose` is a whitelist and it defaults to `livez,readyz,info`.** An endpoint that is not
named is never registered, so it answers 404. Metrics, the log level endpoint and pprof are all off
until an operator names them.

## Considered options

- **Its own port, always.** What the prototype did, on `:9090`. It makes `/metrics` unreachable from
  the public Ingress with no configuration at all. Rejected because it locks out any service on a
  platform that routes one port — Cloud Run, Fly.io — and because Spring Boot's own default is the
  shared port, and Spring Boot developers are the audience.
- **Shared port with no whitelist**, guarded by documenting that the operator must block
  `/actuator/` at the Ingress. Rejected: one wrong Ingress rule then leaks a live write endpoint,
  and the mistake is invisible until someone finds it.
- **A shared secret** on the write endpoints. Rejected because go-boot has no Security Starter in
  v1, so this would be a one-endpoint authentication scheme that has to keep working after the real
  one arrives.

## Consequences

- **Metrics answer 404 until you opt in.** This is the surprise, and it belongs on the first line of
  the Actuator's documentation. An operator wiring Prometheus will hit the 404 before they find the
  key.
- **`actuator.addr` still moves the Actuator to its own listener**, which it then owns and runs
  itself. The whitelist applies in both modes. A setting that changes meaning because another
  setting was set is fine on the day it is written and baffling a year later.
- **An unknown entry in `expose` fails at startup.** ADR 0002 already makes an unknown config key a
  hard error, and a typo in a probe path is a failure worth having at boot.
- **The log level endpoint needs no authentication**, because a service that has not opted in has no
  such route.
- **`/livez` and `/readyz` also answer at the root**, outside the prefix, because Kubernetes reads
  them. One `expose` entry governs both paths for an endpoint.
- **This is public API the moment anyone deploys it.** The path names, the default whitelist and the
  404-not-403 behaviour all become promises. Widening the default later is safe; narrowing it is not.
