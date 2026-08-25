# Deployron: A Lightweight Deploy Tool
[![CI](https://github.com/MegaThorx/deployron/actions/workflows/ci.yml/badge.svg?branch=master)](https://github.com/MegaThorx/deployron/actions/workflows/ci.yml)

_Deployron_ is a small and lightweight deployment tool that is preferably used with Docker, but is suitable for most general purpose deployments as well.  
It uses a _yaml_ configuration file that holds a single or multiple deploy scripts which can be executed with extended privileges in a secure way.

## Architecture
![Architecture Image](https://i.imgur.com/zCq1YLQ.png)

## Installation
1. Download and extract the latest release into `/var/lib/deployron/`.

2. Create the API system group and service account:

   ```bash
   groupadd --system deployron
   useradd --system --gid deployron --home-dir /var/lib/deployron --no-create-home deployron
   ```

   If the `deployron` user already exists but the group does not, create the group and add the existing user to it:

   ```bash
   groupadd --system deployron
   usermod --append --groups deployron deployron
   ```

3. Protect the installation directory and executables. They must not be writable by the unprivileged API account because the backend runs deployment commands as root:

   ```bash
   chown root:deployron /var/lib/deployron
   chmod 750 /var/lib/deployron
   chown root:root /var/lib/deployron/api /var/lib/deployron/service
   chmod 755 /var/lib/deployron/api /var/lib/deployron/service
   ```

4. Create the configuration from the shipped example and adjust it:

   ```bash
   cp /var/lib/deployron/config.example.yml /var/lib/deployron/config.yml
   ```

5. Keep the configuration owned by root, but make it readable by the `deployron` group:

   ```bash
   chown root:deployron /var/lib/deployron/config.yml
   chmod 640 /var/lib/deployron/config.yml
   ```

   The backend requires root ownership because deployment commands can run with elevated privileges. Group-read access allows `deployron_api.service`, which runs as `deployron`, to load the same configuration.

6. Install the _systemd_ services:

   ```bash
   cp /var/lib/deployron/systemd/*.service /etc/systemd/system/
   systemctl daemon-reload
   ```

7. Start and enable the services:

   ```bash
   systemctl enable --now deployron.service
   systemctl enable --now deployron_api.service
   ```

## Configuration
The following snippet is a commented example configuration.
```yml
api:
  ip: 127.0.0.1 # IP the server should listen on (use 0.0.0.0 to listen on all interfaces)
  port: 1337 # Port we're listening on (optional, defaults to 1337)
  # trusted_proxies: # reverse proxies whose X-Forwarded-For header is trusted for rate limiting (optional, default off)
  # - 127.0.0.1
  # - 10.0.0.0/8

service:
  unixsocket: "./service.sock" # The unix backend process server socket (optional)

deployments:
- name: mydeploy1 # name of the deployment entry (you can use any)
  secret: change-me-deploy1 # per deployment secret
  description: "My test deploy service 1" # friendly description (optional)
  user: root # the user who should execute the script below (optional, defaults to root)
  script: # The actual deploy script
  - echo "Hello World from mydeploy1"
  - whoami

- name: mydeploy2
  secret: change-me-deploy2
  description: "My test deploy service 2"
  user: deploy
  cron_deploy: "0 4 * * *" # additionally run this deployment on a cron schedule (optional)
  script:
  - echo "Hello World from mydeploy2"
  - whoami

```

Deploy scripts run under `bash` with `set -euo pipefail`, so a deployment aborts as soon as any step fails. When a trigger arrives while the same deployment is already running, exactly one follow-up run is queued; additional triggers during that time coalesce into it, so bursts never pile up but the latest trigger is never lost.

Only set `trusted_proxies` when a reverse proxy is the sole route to the API and it overwrites or appends `X-Forwarded-For`; the throttle then rate-limits the forwarded client address instead of the proxy's.

## Usage
Trigger a deployment with a `POST` request, passing the secret in the `X-API-Secret` header:

```bash
curl -X POST -H "X-API-Secret: change-me-deploy1" http://127.0.0.1:1337/deploy/mydeploy1
# {"run":42,"status":"queued"}
```

A `200` here only means the deployment was handed off to the backend. For CI pipelines, add `wait=true` to block until the deployment finishes and get its outcome as the HTTP status — combined with `--fail`, curl exits non-zero and breaks the pipeline when the deploy script fails:

```bash
curl --fail -X POST -H "X-API-Secret: change-me-deploy1" "http://127.0.0.1:1337/deploy/mydeploy1?wait=true"
# {"duration_ms":5300,"run":42,"status":"success"}     (200 on success, 502 on failure, 504 after 15 min)
```

You can also inspect a deployment's last run at any time:

```bash
curl -H "X-API-Secret: change-me-deploy1" http://127.0.0.1:1337/deploy/mydeploy1/status
# {"state":"idle","last":"success","last_run":42,"finished":1724600000,"dur_ms":5300}
```

Run status is kept in memory and resets when the backend service restarts. Unknown deployment names and wrong secrets both return the same `404`, and repeated failed attempts from the same address are temporarily blocked with `429`.

The secret must be sent in the `X-API-Secret` header; the old `?APISecret=` query parameter is no longer accepted, because query strings end up in access and proxy logs. If you expose the API beyond localhost, put it behind a TLS-terminating reverse proxy (and see `trusted_proxies` above).
