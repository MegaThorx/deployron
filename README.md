# Deployron: A Lightweight Deploy Tool
[![CI](https://github.com/MegaThorx/deployron/actions/workflows/ci.yml/badge.svg?branch=master)](https://github.com/MegaThorx/deployron/actions/workflows/ci.yml)

_Deployron_ is a small and lightweight deployment tool that is preferably used with Docker, but is suitable for most general purpose deployments as well.  
It uses a _yaml_ configuration file that holds a single or multiple deploy scripts which can be executed with extended privileges in a secure way.

## Architecture
![Architecture Image](http://i.imgur.com/zCq1YLQ.png)

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

4. Update `/var/lib/deployron/config.yml`.

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

service:
  unixsocket: "./service.sock" # The unix backend process server socket (optional)

deployments:
- name: mydeploy1 # name of the deployment entry (you can use any)
  secret: deploy1secret # per deployment secret
  description: "My test deploy service 1" # friendly description (optional)
  user: root # the user who should execute the script below (optional, defaults to root)
  script: # The actual deploy script
  - echo "Hello World from mydeploy1"
  - whoami

- name: mydeploy2
  secret: deploy2secret
  description: "My test deploy service 2"
  user: vm
  script:
  - echo "Hello World from mydeploy2"
  - whoami

```

API clients use unnamed Unix sockets. The old `api.unixsocket` setting is accepted for compatibility but is ignored and can be removed.
