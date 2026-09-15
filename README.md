**Daggerer** enables automation for building a `Dockerfile`, publishing the image to an OCR registry, and deploying the image hosted on that OCR registry using `ssh` and `docker compose` [with Dagger](https://dagger.io).

## Quick Reference

Daggerer exposes four API functions:

- `build-only` builds an input `Dockerfile` with [`Directory.DockerBuild`][dagger-build] and returns a cached [`Container`][dagger-container].
- `build` validates registry authentication, calls `build-only`, applies [`WithRegistryAuth`](https://docs.dagger.io/reference/api/container#withRegistryAuth), and [`Publish`](https://docs.dagger.io/reference/api/container#publish) publishes the image.
- `deploy` connects to a target `ssh` server, pulls an existing image, and uses Compose to serve the image.
- `release` calls `build` and then `deploy` with the same registry, application name, and tag simplifying the top level API.

```bash
dagger -W github.com/ninesl/daggerer@master api functions
dagger -W github.com/ninesl/daggerer@master api call build-only --help
dagger -W github.com/ninesl/daggerer@master api call build --help
dagger -W github.com/ninesl/daggerer@master api call deploy --help
dagger -W github.com/ninesl/daggerer@master api call release --help

# The command shape is always:
dagger -W github.com/ninesl/daggerer@master api call function --arguments
```

## Contents

The documentation below uses Github Action examples on a self-hosted GitHub runner.

- [Quick Reference](#quick-reference)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Staging And Production](#staging-and-production)
- [Build Inputs](#build-inputs)
- [How Deployment Works](#how-deployment-works)
- [Caching](#caching)
- [Workspace Usage](#workspace-usage)

## Prerequisites

> Read through the documentation before adapting an example. The examples favor [grug-brained](https://grugbrain.dev/) development and [locality of behavior](https://htmx.org/essays/locality-of-behaviour/): each workflow passes its choices directly to the Daggerer API.

Daggerer needs a `Dockerfile` to build the application. `deploy` and `release` also require one of these combinations on the deployment host:

- [`docker`](https://docs.docker.com/engine/install/) and [`docker compose`](https://docs.docker.com/compose/install/linux/)
- [`podman`](https://podman.io/docs/installation) and [`podman compose`](https://docs.podman.io/en/latest/markdown/podman-compose.1.html)

`build-only` and `build` do not require `docker` or `podman`, they use the Dagger Engine directly.

Create a self-hosted runner for your application's repository at `https://github.com/<owner>/<repo>/settings/actions/runners/new`. Install and start it according to GitHub's instructions. This location is your choice; Daggerer does not require a runner filesystem layout.

From the directory where you installed the runner, the GitHub setup instructions use:

```bash
sudo ./svc.sh install "$USER"
sudo ./svc.sh start
sudo ./svc.sh status
```

[Install the Dagger CLI](https://docs.dagger.io/getting-started/install) for the Linux user that owns the runner service. Daggerer was built with Dagger `v1.0.0-beta.13`; run `dagger version` as the user to verify access to the CLI and Dagger Engine.

## Quick Start

This example builds and publishes the checked-out application, then deploys it back to the same VPS that runs the GitHub runner. It uses `docker`, the default `Dockerfile`, the default `compose.yml`, and the default `latest` tag.

For the Quick Start only, we use the following example filesystem. These paths are ordinary API inputs, not a layout required by Daggerer:

```text
/home/runner/
├── secrets-actions/
│   └── my-app/
│       ├── registry_password
│       ├── ssh_key
│       └── known_hosts
├── actions-runner/
│   └── my-app/              # GitHub Actions self-hosted runner for my-app
│       ├── run.sh
│       └── svc.sh
└── apps/
    └── my-app/
        └── compose.yml
```

Place this file at `/home/runner/apps/my-app/compose.yml`:

```yaml
services:
  app:
    image: ${APP_IMAGE:?APP_IMAGE is required}
    restart: unless-stopped
    ports:
      - "5000:5000"
```

The application must listen on port `5000` inside its container. The service is then available on port `5000` of the VPS. 

> In production I use [`caddy`](https://caddyserver.com/docs/quick-starts/reverse-proxy) (simplified replacement for [`nginx`](https://docs.nginx.com/nginx/admin-guide/web-server/reverse-proxy/)) as a reverse proxy.

The example keeps credential files in `$HOME/secrets-actions/my-app` on the VPS. The runner service user must be able to read them. Typed Dagger `Secret` arguments use Dagger's [`file://` secret provider](https://docs.dagger.io/adopting/secrets). Plain string settings are declared in the workflow.

```yaml
name: Release

on:
  push:
    branches: [master, main]

permissions:
  contents: read

jobs:
  release:
    runs-on: [self-hosted, linux, x64]
    env:
      APP_NAME: ${{ github.event.repository.name }}
      REGISTRY_URL: registry.example.com/team
      REGISTRY_USERNAME: registry-user
      SSH_TARGET: runner@runner.example.com
    steps:
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Build, publish, and deploy
        shell: bash
        run: |
          set -euo pipefail
          secrets_dir="$HOME/secrets-actions/$APP_NAME"
          dagger -W github.com/ninesl/daggerer@master api call release \
            --source=. \
            --registry="$REGISTRY_URL" \
            --app-name="$APP_NAME" \
            --registry-username="$REGISTRY_USERNAME" \
            --registry-password="file://$secrets_dir/registry_password" \
            --ssh-target="$SSH_TARGET" \
            --ssh-key="file://$secrets_dir/ssh_key" \
            --known-hosts="file://$secrets_dir/known_hosts" \
            --deploy-directory="apps/$APP_NAME" \
            --deploy-container-runtime=docker
```

Each argument has one job:

| Argument | Quick Start value |
| --- | --- |
| `--source` | The checked-out application and Docker build context |
| `--registry` | Registry host and namespace from `REGISTRY_URL` |
| `--app-name` | The GitHub repository name, producing `<REGISTRY_URL>/my-app:latest` |
| `--registry-username` / `--registry-password` | Credentials used to verify access, publish, and log in on the deployment host |
| `--ssh-target` | The `ssh` user and host where Compose runs |
| `--ssh-key` / `--known-hosts` | `ssh` authentication and verified server identity read from the runner |
| `--deploy-directory` | `apps/my-app` under the `ssh` user's home |
| `--deploy-container-runtime` | Selects `docker` for remote login, pull, and Compose |

`release` passes the same `--registry`, `--app-name`, and tag to both `build` and `deploy`. It defaults to `--tag=latest`, `--dockerfile=Dockerfile`, and `--compose-file=compose.yml`. With `REGISTRY_URL=registry.example.com/team`, it publishes and pulls `registry.example.com/team/my-app:latest`, sets `APP_IMAGE` for Compose, and runs:

```bash
cd "$HOME/apps/my-app" && \
  APP_IMAGE=registry.example.com/team/my-app:latest \
  docker compose -f compose.yml up -d --force-recreate --remove-orphans
```

Any failed registry, build, publish, `ssh`, pull, or Compose step stops the release.

## Staging And Production

The following pair is a practical extension of Quick Start:

| | Staging | Production |
| --- | --- | --- |
| Trigger | Branches other than `master`/`main` | `master` and `main` |
| Deployment host | The local runner VPS | An external `ssh` server |
| Runtime | Podman | Docker |
| Tag | Commit SHA | Commit SHA |
| Directory | `$HOME/apps/my-app/staging` | `$HOME/apps/my-app/production` |

Both hosts use the same Compose file shape. Place it at the selected deployment directory as `compose.yml`:

```yaml
services:
  app:
    image: ${APP_IMAGE:?APP_IMAGE is required}
    restart: unless-stopped
    ports:
      - "5000:5000"
    env_file:
      - runtime.env
    secrets:
      - app_token

secrets:
  app_token:
    file: ./secrets/app_token
```

Provision `runtime.env` and `secrets/app_token` independently on each host. They configure the running application and never enter the Dagger build. See Compose documentation for [`env_file`](https://docs.docker.com/reference/compose-file/services/#env_file) and [`secrets`](https://docs.docker.com/reference/compose-file/secrets/).

In this example, the runner VPS also hosts staging. We chose the following filesystem to keep each application's runner, credentials, and Compose project together. Daggerer does not require these directory names:

```text
/home/runner/
├── secrets-actions/
│   └── my-app/
│       ├── registry_password
│       ├── staging_ssh_key
│       ├── staging_known_hosts
│       ├── production_ssh_key
│       └── production_known_hosts
├── actions-runner/
│   └── my-app/              # GitHub Actions self-hosted runner for my-app
│       ├── run.sh
│       └── svc.sh
└── apps/
    └── my-app/
        └── staging/
            ├── compose.yml
            ├── runtime.env
            └── secrets/
                └── app_token
```

The external production host contains only its independently provisioned Compose project and runtime secrets:

```text
/home/deploy/
└── apps/
    └── my-app/
        └── production/
            ├── compose.yml
            ├── runtime.env
            └── secrets/
                └── app_token
```

The staging public key is authorized for the runner user on the runner VPS. The production public key is authorized for the deployment user on the external server. Each `known_hosts` file contains the verified key for only its target.

### Staging: Local Runner VPS With Podman

The staging workflow executes on the runner VPS and connects back to that VPS with `ssh`. The `ssh` hop is intentional: `deploy` follows the same path for local staging and remote production.

```yaml
name: Deploy staging

on:
  push:
    branches-ignore: [master, main]

permissions:
  contents: read

jobs:
  release:
    runs-on: [self-hosted, linux, x64]
    env:
      APP_NAME: ${{ github.event.repository.name }}
      REGISTRY_URL: registry.example.com/team
      REGISTRY_USERNAME: registry-user
      SSH_TARGET: runner@runner.example.com
    steps:
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Release to staging
        shell: bash
        run: |
          set -euo pipefail
          secrets_dir="$HOME/secrets-actions/$APP_NAME"
          dagger -W github.com/ninesl/daggerer@master api call release \
            --source=. \
            --registry="$REGISTRY_URL" \
            --app-name="$APP_NAME" \
            --tag="$GITHUB_SHA" \
            --registry-username="$REGISTRY_USERNAME" \
            --registry-password="file://$secrets_dir/registry_password" \
            --ssh-target="$SSH_TARGET" \
            --ssh-key="file://$secrets_dir/staging_ssh_key" \
            --known-hosts="file://$secrets_dir/staging_known_hosts" \
            --deploy-directory="apps/$APP_NAME/staging" \
            --deploy-container-runtime=podman
```

- `--tag="$GITHUB_SHA"` gives each staging build an immutable commit-specific image tag.
- `--ssh-target` points back to the runner VPS using an address reachable from Dagger's container network.
- `--ssh-key` and `--known-hosts` select the staging credentials on that same VPS.
- `--deploy-directory` selects the staging Compose project under the runner user's home.
- `--deploy-container-runtime=podman` runs `podman login`, `podman pull`, and `podman compose` on staging.

### Production: External `ssh` Server With Docker

Production uses the same runner to build and publish, then connects with `ssh` to another server where Docker runs the application.

The staging and production workflows use separate target, key, and `known_hosts` files on the example VPS. They share the registry files because one `release` uses the same registry to publish and pull its image.

```yaml
name: Deploy production

on:
  push:
    branches: [master, main]

permissions:
  contents: read

jobs:
  release:
    runs-on: [self-hosted, linux, x64]
    env:
      APP_NAME: ${{ github.event.repository.name }}
      REGISTRY_URL: registry.example.com/team
      REGISTRY_USERNAME: registry-user
      SSH_TARGET: deploy@prod.example.com
    steps:
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Release to production
        shell: bash
        run: |
          set -euo pipefail
          secrets_dir="$HOME/secrets-actions/$APP_NAME"
          dagger -W github.com/ninesl/daggerer@master api call release \
            --source=. \
            --registry="$REGISTRY_URL" \
            --app-name="$APP_NAME" \
            --tag="$GITHUB_SHA" \
            --registry-username="$REGISTRY_USERNAME" \
            --registry-password="file://$secrets_dir/registry_password" \
            --ssh-target="$SSH_TARGET" \
            --ssh-key="file://$secrets_dir/production_ssh_key" \
            --known-hosts="file://$secrets_dir/production_known_hosts" \
            --deploy-directory="apps/$APP_NAME/production" \
            --deploy-container-runtime=docker
```

- `--source`, registry arguments, `--app-name`, and `--tag` build the same application image policy as staging.
- `--ssh-target` names the external production user and server.
- `--ssh-key` is a production-only private key whose public key is authorized on that server.
- `--known-hosts` verifies the external server instead of the local runner VPS.
- `--deploy-directory` selects the production Compose project under the production `ssh` user's home.
- `--deploy-container-runtime=docker` runs `docker login`, `docker pull`, and `docker compose` on production.

The workflow reads deployment inputs from the example VPS filesystem. The Compose projects, `runtime.env`, and application runtime secrets exist on their respective deployment hosts.

## Build Inputs

The examples above need no additional Dockerfile inputs. Daggerer also supports public build values, named build secrets, and a private dotenv secret.

| Argument | Purpose |
| --- | --- |
| `--build-env-file` | Public dotenv file converted to Dockerfile build arguments |
| `--build-values` | Public Dagger `EnvFile` supplied by an API client; overrides duplicate file values |
| `--build-secret-ids` / `--build-secrets` | BuildKit secret IDs paired with Dagger Secrets by position |
| `--build-secret-env` | Private dotenv mounted with the fixed BuildKit ID `build_env` |
| `--dockerfile` | Dockerfile path relative to `--source`; defaults to `Dockerfile` |

Build values are not secrets. Runtime values and secrets belong in the deployment host's Compose project, not in Docker build arguments.

### Shared Dockerfile Example

This Go example derives `GO_VERSION` from the application's `go.mod`, downloads a private GitHub module with a PAT, accepts another build secret named `BAR`, and bakes the public `FOO` value into the image:

```dockerfile
# syntax=docker/dockerfile:1.7

ARG GO_VERSION
FROM golang:${GO_VERSION}-alpine AS builder

RUN apk add --no-cache ca-certificates git
WORKDIR /app

COPY go.mod go.sum ./
ARG GOPRIVATE=github.com/your-org/*
ENV GOPRIVATE=$GOPRIVATE
RUN --mount=type=secret,id=github_token,required=true \
    set -eu; \
    token="$(cat /run/secrets/github_token)"; \
    GIT_CONFIG_COUNT=1 \
    GIT_CONFIG_KEY_0="url.https://x-access-token:${token}@github.com/.insteadOf" \
    GIT_CONFIG_VALUE_0="https://github.com/" \
    go mod download

COPY . .
ARG APP_PACKAGE=.
RUN --mount=type=secret,id=BAR,required=true \
    test -s /run/secrets/BAR && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -o /app/server "$APP_PACKAGE"

FROM alpine:3.24.1
ARG FOO=hello
ENV FOO=$FOO
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/server /usr/local/bin/server
ENTRYPOINT ["/usr/local/bin/server"]
```

Create a [fine-grained GitHub PAT](https://github.com/settings/personal-access-tokens/new) with read access to the private dependency repository. Store its raw value and the `BAR` value in separate runner-local files. Then add these arguments to either release call:

```bash
go_version="$(awk '$1 == "go" { print $2 }' go.mod)"
: "${go_version:?go.mod must contain a go directive}"
build_env_file="$(mktemp)"
trap 'rm -f "$build_env_file"' EXIT
printf 'GO_VERSION=%s\nAPP_PACKAGE=.\nFOO=hello\n' "$go_version" > "$build_env_file"

dagger -W github.com/ninesl/daggerer@master api call release \
  --source=. \
  --build-env-file="$build_env_file" \
  --build-secret-ids=github_token,BAR \
  --build-secrets=file://$HOME/secrets-actions/my-app/github_token,file://$HOME/secrets-actions/my-app/bar \
  <the registry and deployment arguments from the selected workflow>
```

The two secret lists must have equal lengths. `github_token` maps to the first file and `BAR` maps to the second. The IDs must match the Dockerfile's secret mounts. Add `.env`, `.git`, and any runner credential paths to `.dockerignore` when applicable.

### Private Dotenv Example

`--build-secret-env` mounts one private dotenv file under BuildKit ID `build_env`. A Dockerfile can consume it like this:

```dockerfile
RUN --mount=type=secret,id=build_env,required=true \
    set -eu; \
    . /run/secrets/build_env; \
    test -n "$BAR"
```

Pass it as a Secret:

```bash
--build-secret-env=file://$HOME/secrets-actions/my-app/private.env
```

The mount exists only for the `RUN` instruction that requests it. It is not copied into the resulting image.

## How Deployment Works

`deploy` creates a temporary Alpine `ssh` client inside Dagger and mounts the `ssh` key, `known_hosts`, and registry password as Dagger [`Secret`][dagger-secret] values. It then runs four remote steps:

```text
1. Verify <deploy-directory>/<compose-file> exists.
2. Run docker|podman login with the registry password on stdin.
3. Pull <registry>/<app-name>:<tag>.
4. Set APP_IMAGE and run docker|podman compose up.
```

The resulting path is:

```text
self-hosted runner
    -> Dagger Engine
        -> temporary `ssh` client container
            -> `ssh` deployment host
                -> Docker or Podman Compose
```

`--deploy-directory` is relative to the `ssh` user's home. `--compose-file` defaults to `compose.yml`. Daggerer passes `APP_IMAGE=<registry>/<app-name>:<tag>` to Compose; the examples consume it with `image: ${APP_IMAGE:?APP_IMAGE is required}`.

Strict `ssh` host-key checking uses the supplied `--known-hosts` Secret. The `ssh` target must be reachable from Dagger's container network even when it is the runner VPS itself.

## Caching

| Work | Behavior |
| --- | --- |
| `build-only` / [`DockerBuild`][dagger-build] | Reuses eligible cached work for unchanged inputs |
| Registry authentication | Runs on every build or release |
| [`Publish`][dagger-publish] | Publishes on every build or release; existing blobs may be reused |
| `ssh` checks, login, pull, and Compose | Run on every deploy or release |

This keeps expensive image builds cacheable while ensuring registry and deployment side effects happen for every requested release.

## Workspace Usage

Load Daggerer directly from Git:

```bash
dagger -W github.com/ninesl/daggerer@master api call release --help
```

The consumer needs no `dagger.toml` or `dagger.lock`. To install Daggerer in a consumer workspace instead:

```bash
dagger init
dagger module install github.com/ninesl/daggerer@master
dagger api call daggerer release --help
```

The installed module is namespaced as `daggerer`; the `-W` form selects Daggerer as the entrypoint and calls `release` directly.

[dagger-build]: https://docs.dagger.io/reference/api/directory#dockerBuild
[dagger-container]: https://docs.dagger.io/reference/api/container
[dagger-publish]: https://docs.dagger.io/reference/api/container#publish
[dagger-secret]: https://docs.dagger.io/reference/api/secret
