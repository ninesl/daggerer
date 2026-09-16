**Daggerer** builds a `Dockerfile`, publishes its image to an OCI registry, and deploys it over `ssh` with `docker compose` or `podman compose` [using Dagger](https://dagger.io).

## Quick Reference

Daggerer exposes four API functions:

- `build-only` builds an input `Dockerfile` with [`Directory.DockerBuild`][dagger-build] and returns a cached [`Container`][dagger-container].
- `build` validates registry authentication, calls `build-only`, applies [`WithRegistryAuth`](https://docs.dagger.io/reference/api/container#withRegistryAuth), and [`Publish`](https://docs.dagger.io/reference/api/container#publish) publishes the image.
- `deploy` connects to the `ssh` target, pulls an existing image, and runs the file selected by `--compose-file` (`compose.yml` by default) with `docker compose` or `podman compose`.
- `release` calls `build` and then `deploy` with the same registry, application name, and tag simplifying the top level API.

```bash
# List the available Daggerer functions.
dagger -W github.com/ninesl/daggerer@master api functions
# Inspect each function's parameters and defaults.
dagger -W github.com/ninesl/daggerer@master api call build-only --help
dagger -W github.com/ninesl/daggerer@master api call build --help
dagger -W github.com/ninesl/daggerer@master api call deploy --help
dagger -W github.com/ninesl/daggerer@master api call release --help

# The command shape is always:
dagger -W github.com/ninesl/daggerer@master api call function --arguments
```

## Contents

The examples describe our sample application, `my-app`, using GitHub Actions on a self-hosted runner.

> Read through the documentation before adapting an example. The examples favor [grug-brained](https://grugbrain.dev/) development and [locality of behavior](https://htmx.org/essays/locality-of-behaviour/): each workflow passes its choices directly to the Daggerer API.

- [Quick Reference](#quick-reference)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Staging And Production](#staging-and-production)
- [Build Without Deployment](#build-without-deployment)
- [GitHub Actions Secrets As Environment Inputs](#github-actions-secrets-as-environment-inputs)
- [How Deployment Works](#how-deployment-works)
- [Caching](#caching)
- [Workspace Usage](#workspace-usage)

## Prerequisites

`deploy` and `release` require either:

- [`docker`](https://docs.docker.com/engine/install/) and [`docker compose`](https://docs.docker.com/compose/install/linux/) 

or 

- [`podman`](https://podman.io/docs/installation) and [`podman compose`](https://docs.podman.io/en/latest/markdown/podman-compose.1.html) 

installed on the `ssh` target. This does NOT need to match Dagger's runtime, Dagger just needs to build and publish an OCR image.

Daggerer needs a `Dockerfile` to build the image.

`build-only` and `build` **DO NOT** require `docker` or `podman`, they use the self-hosted runner's Dagger Engine directly.

> [TODO: An external Dagger Engine target API is planned](link to dagger.io docs on how a dagger cli can connect to an external (like remote) dagger engine)

Create a self-hosted runner for your application's repository at `https://github.com/<owner>/<repo>/settings/actions/runners/new`. 

Install and start the runner according to GitHub's instructions. This location is your choice; Daggerer can be run anywhere that has access to the Dagger Engine.

From the directory where you installed the runner, the GitHub setup instructions use for persistence:

```bash
# Ran from the runner installation directory
# The current user (which will use Dagger CLI) is our runner's user
sudo ./svc.sh install "$USER"
# Start the runner
sudo ./svc.sh start
# Inspect the runner's status
sudo ./svc.sh status
```

[Install the Dagger CLI](https://docs.dagger.io/getting-started/install) for the same `$USER` as your runner service. 

Daggerer was built with Dagger `v1.0.0-beta.13`; run `dagger version` as the user to verify access to the CLI and Dagger Engine.

## Quick Start

This example uses a VPS that serves multiple roles:
- self-hosted GitHub Actions runner
- deploy the image via `docker compose`

> The environment is essentially an [AWS EC2](https://aws.amazon.com/ec2/) instance or similar VPS.

The following layout is **not** required by Daggerer, or really reccommended. My intention with this example is to highlight how inputs can be sourced from anywhere to be used by Daggerer.

These filesystem paths become Daggerer's API inputs.

```bash
# VPS filesystem
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
        └── compose.yml     # using Daggerer's default
```

```yaml
# /home/runner/apps/my-app/compose.yml on the `ssh` target (the runner VPS).
services:
  app:
    # docker compose -f compose.yml substitutes APP_IMAGE; missing/empty values fail.
    # APP_IMAGE is unqiue to our compose.yml, explicitly passed with --deploy-values below.
    image: ${APP_IMAGE:?APP_IMAGE is required}
    restart: unless-stopped
    ports:
      # Publish `ssh` target port 5000 to the application's listening port 5000.
      - "5000:5000"
```

> In production I use [`caddy`](https://caddyserver.com/docs/quick-starts/reverse-proxy) (simplified replacement for [`nginx`](https://docs.nginx.com/nginx/admin-guide/web-server/reverse-proxy/)) as a reverse proxy.

```yaml
# my-repo/.github/workflows/release.yml
name: Release

# Release the main/master checkout on pushes to either branch.
on:
  push:
    branches: [master, main]

permissions:
  contents: read

jobs:
  release:
    runs-on: [self-hosted, linux, x64]
    env:
      # Our example application name matches the my-app paths on the VPS.
      APPLICATION_NAME: my-app
    steps:
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Build, publish, and deploy
        env:
          # Our application's deployment variable; use the same image reference we publish
          # Daggerer forwards these public values to the remote Compose process
          DEPLOY_VALUES: |
            APP_IMAGE=registry.example.com/team/${{ env.APPLICATION_NAME }}:latest
        run: |
          dagger -W github.com/ninesl/daggerer@master api call release
            # Supply the step's current directory as the Docker build context
            # Here it contains the repository files placed by actions/checkout
            --source=.
            # Registry host and image namespace
            --registry=registry.example.com/team
            # The image will be registry.example.com/team/my-app:latest
            --app-name="$APPLICATION_NAME"
            # Account used for publishing and remote registry login.
            --registry-username=registry-user
            # file:// loads the runner-local file as a Dagger Secret.
            --registry-password="file://$HOME/secrets-actions/$APPLICATION_NAME/registry_password"
            # The `ssh` target is the runner VPS, using the runner user.
            --ssh-target=runner@runner.example.com
            # `ssh` key whose public key is authorized for the `ssh` target user.
            --ssh-key="file://$HOME/secrets-actions/$APPLICATION_NAME/ssh_key"
            # `known_hosts` file containing the verified host key for the `ssh` target.
            --known-hosts="file://$HOME/secrets-actions/$APPLICATION_NAME/known_hosts"
            # Relative to the `ssh` target user's home, not the runner's checkout.
            # Selects /home/runner/apps/my-app on this `ssh` target, contains compose.yml
            --deploy-directory="apps/$APPLICATION_NAME"
            # Forward our variables to docker compose -f compose.yml up on the `ssh` target.
            # APP_IMAGE exists here only because we pass it; --tag does not set it.
            --deploy-values="$DEPLOY_VALUES"
            # Our VPS will use docker login, docker pull, and docker compose -f compose.yml up.
            --deploy-container-runtime=docker
```

- Omitted `--dockerfile`: uses `Dockerfile` at the root of `--source`.
- Omitted `--tag`: uses `latest`.
- Omitted `--compose-file`: uses `compose.yml`.

```bash
# Equivalent final deployment command on the Quick Start `ssh` target.
# $HOME belongs to the `ssh` target user; compose.yml is in this directory.
# Our workflow explicitly passes APP_IMAGE through --deploy-values to select this image.
# Recreate the service in the background and remove services no longer in compose.yml.
cd "$HOME/apps/my-app" && \
  APP_IMAGE=registry.example.com/team/my-app:latest \
  docker compose -f compose.yml up -d --force-recreate --remove-orphans
```

Any failed registry, build, publish, `ssh`, pull, or `docker compose -f compose.yml up` step stops this release.

## Staging And Production

Our application extends Quick Start with two workflows calling the same `release` API. Each workflow chooses its build inputs, image tag, `ssh` target, Compose file, and container runtime through parameters. Here we share the private build file containing our dependency PAT, override public build values per workflow, and give each `ssh` target its own runtime variables and runtime secrets. These are our application's choices; the same API can support other layouts and environments.

### Shared Application Files

These are our application's chosen files and variable names. Both workflows below select them explicitly; their deployment files use standard Compose [`env_file`](https://docs.docker.com/reference/compose-file/services/#env_file) and [`secrets`](https://docs.docker.com/reference/compose-file/secrets/).

```dockerfile
# syntax=docker/dockerfile:1.7
# myapp.Dockerfile in our application's checkout; selected by both release workflows.

# Public toolchain version from build.env, matching our application's go.mod.
ARG GO_VERSION
FROM golang:${GO_VERSION}-alpine AS builder

RUN apk add --no-cache ca-certificates git
WORKDIR /app
COPY go.mod go.sum ./
# Our application's private dependency namespace.
ARG GOPRIVATE=github.com/your-org/*
ENV GOPRIVATE=$GOPRIVATE
# --build-secret-env supplies this temporary mount from a caller-selected file.
# Read GITHUB_PAT from shell-compatible assignments; use it only for dependency downloads.
# The secret mount is not copied into the image.
RUN --mount=type=secret,id=build_env,required=true \
    set -eu; \
    . /run/secrets/build_env; \
    GIT_CONFIG_COUNT=1 \
    GIT_CONFIG_KEY_0="url.https://x-access-token:${GITHUB_PAT}@github.com/.insteadOf" \
    GIT_CONFIG_VALUE_0="https://github.com/" \
    go mod download

COPY . .
# Public package selection from build.env or --build-values.
ARG APP_PACKAGE=.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -o /app/server "$APP_PACKAGE"

FROM alpine:3.24.1
# Our application deliberately bakes this public build value into its runtime environment.
# Staging overrides it with staging; production overrides it with production.
ARG FOO=hello
ENV FOO=$FOO
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/server /usr/local/bin/server
# Start our application when dev.compose.yml or prod.compose.yml runs this image.
ENTRYPOINT ["/usr/local/bin/server"]
```

```dotenv
# build.env in our application's checkout; public build defaults for both workflows.
# Match our application's go.mod toolchain version.
GO_VERSION=1.26.8
# Our application's main Go package.
APP_PACKAGE=.
# Public value baked into the image; each workflow overrides it through --build-values.
FOO=hello
```

```dotenv
# $HOME/secrets-actions/my-app/private.env on the runner VPS; shared by both builds.
# --build-secret-env mounts the whole file at /run/secrets/build_env during the build.
# Shell-compatible assignments: myapp.Dockerfile sources the file without public build args.
# Example PAT with read access to our private dependency repository; not a runtime token.
GITHUB_PAT=example-token
```

```dockerignore
# .dockerignore in our application's checkout: exclude metadata and local secrets.
.git
.env
private.env
secrets-actions/
# build.env remains available as public build configuration.
```

```dotenv
# deploy.env in our application's checkout on the runner VPS.
# --deploy-env-file forwards public deployment process variables to docker/podman compose.
# Our application chooses this standard Compose setting; Daggerer treats every key generically.
COMPOSE_PARALLEL_LIMIT=4
# Each workflow explicitly supplies APP_IMAGE through --deploy-values, not through Daggerer defaults.
# Application runtime variables and secrets are selected separately by dev.compose.yml/prod.compose.yml.
```

For our application, the runner VPS also serves as the staging `ssh` target. This is our chosen filesystem layout:

```text
# Our chosen runner VPS layout: credential files are read by the runner service user.
/home/runner/
├── secrets-actions/
│   └── my-app/
│       ├── registry_password
│       ├── private.env       # Shared private build file containing GITHUB_PAT
│       ├── staging_ssh_key
│       ├── staging_known_hosts
│       ├── production_ssh_key
│       └── production_known_hosts
├── actions-runner/
│   └── my-app/              # GitHub Actions self-hosted runner for my-app
│       ├── run.sh
│       └── svc.sh
└── staging/
    └── my-app/
        ├── dev.compose.yml
        ├── .env
        └── secrets/
            └── app_token
```

### Staging: Runner VPS As The `ssh` Target With Podman

The staging workflow executes on the runner VPS and connects to the staging `ssh` target on that same runner VPS. `deploy` uses the same `ssh` connection flow for both staging and production.

```yaml
# my-repo/.github/workflows/staging.yml
# GitHub selects this workflow by branch; Daggerer receives the explicit inputs below.
name: Deploy staging

# Branch pushes update the same staging deployment using their exact commit tags.
on:
  push:
    # Branch pushes except main/master invoke this staging release.
    branches-ignore: [master, main]

permissions:
  contents: read

jobs:
  release:
    # Execute this workflow on the runner VPS; --ssh-target selects where deployment runs.
    runs-on: [self-hosted, linux, x64]
    env:
      # Same application name as the my-app paths on the VPS.
      APPLICATION_NAME: my-app
    steps:
      # Place the triggering commit's repository files in the runner workspace.
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Release to staging
        env:
          # Our application's public staging build override; not a runtime setting.
          BUILD_VALUES: |
            FOO=staging
          # Our application's deployment variable, explicitly matching the image built below.
          # Use the full commit hash here as well as in --tag; neither input sets the other.
          DEPLOY_VALUES: |
            APP_IMAGE=registry.example.com/team/${{ env.APPLICATION_NAME }}:${{ github.sha }}
        run: |
          dagger -W github.com/ninesl/daggerer@master api call release
            # Supply the step's current directory as the Docker build context.
            # Here it contains the branch commit's files placed by actions/checkout.
            --source=.
            # Select myapp.Dockerfile relative to --source.
            # Daggerer builds the image using this file's instructions; production selects it too.
            --dockerfile=myapp.Dockerfile
            # Public build defaults from the application checkout.
            --build-env-file=build.env
            # Our hardcoded staging override takes precedence over build.env.
            --build-values="$BUILD_VALUES"
            # Shared private build file on the runner VPS; also used by production.
            --build-secret-env="file://$HOME/secrets-actions/$APPLICATION_NAME/private.env"
            # Publish and pull from this registry and namespace.
            --registry=registry.example.com/team
            # Our application name becomes the image name: my-app.
            --app-name="$APPLICATION_NAME"
            # Use this exact full commit hash; no shortening or prefix.
            # This controls the image published and pulled, not variables in dev.compose.yml.
            --tag=${{ github.sha }}
            # Registry account used for publish and deployment.
            --registry-username=registry-user
            # Shared registry password file on the runner VPS.
            --registry-password="file://$HOME/secrets-actions/$APPLICATION_NAME/registry_password"
            # Staging `ssh` target on the runner VPS; must be reachable from Dagger.
            --ssh-target=runner@runner.example.com
            # Staging `ssh` key whose public key is authorized for the staging `ssh` target user.
            --ssh-key="file://$HOME/secrets-actions/$APPLICATION_NAME/staging_ssh_key"
            # `known_hosts` file containing the verified host key for the staging `ssh` target.
            --known-hosts="file://$HOME/secrets-actions/$APPLICATION_NAME/staging_known_hosts"
            # Relative to the staging `ssh` target user's home, not the runner's checkout.
            # Selects /home/runner/staging/my-app on the staging `ssh` target.
            # All branch releases update this directory; tags do not create per-branch deployments.
            --deploy-directory="staging/$APPLICATION_NAME"
            # Public process env defaults from our checkout's deploy.env; parsed by Dagger.
            # This is a runner-side input, separate from .env read by dev.compose.yml on staging.
            --deploy-env-file=deploy.env
            # Forward our APP_IMAGE to podman compose -f dev.compose.yml up.
            # Overrides matching deploy.env entries; no application variable names are automatic.
            --deploy-values="$DEPLOY_VALUES"
            # Select dev.compose.yml inside --deploy-directory.
            # Its env_file and secrets entries select staging's .env and secrets/app_token.
            --compose-file=dev.compose.yml
            # Run podman login, podman pull, and podman compose -f dev.compose.yml up.
            --deploy-container-runtime=podman
```

All optional inputs are supplied above. If omitted:

- `--dockerfile`: uses `Dockerfile` instead of `myapp.Dockerfile`.
- `--tag`: uses `latest` instead of the commit hash.
- `--compose-file`: uses `compose.yml` instead of `dev.compose.yml`.
- `--build-env-file`: no public build file is read; explicit build values still apply.
- `--build-values`: no overrides; the public build file's values still apply.
- `--build-secret-env`: no private build file is mounted; our Dockerfile requires it.
- `--deploy-env-file`: no public deployment file is read; explicit deployment values still apply.
- `--deploy-values`: no overrides; the deployment file's values still apply. Our example would need another source for `APP_IMAGE`.

Daggerer does not auto-discover these input files. The runtime `.env` and `secrets/app_token` are selected separately by `dev.compose.yml` on staging.

```yaml
# /home/runner/staging/my-app/dev.compose.yml on the staging `ssh` target.
services:
  app:
    # podman compose -f dev.compose.yml substitutes APP_IMAGE; missing/empty values fail.
    # Our workflow passes it through --deploy-values with the full commit tag.
    # APP_IMAGE is our application's convention; any caller can supply it without Daggerer.
    # It selects an image, not a build argument or application environment variable.
    image: ${APP_IMAGE:?APP_IMAGE is required}
    # Restart unless explicitly stopped.
    restart: unless-stopped
    ports:
      # Publish staging port 5000 to our application's listening port 5000.
      - "5000:5000"
    env_file:
      # Read .env beside dev.compose.yml and inject its values into the application container.
      - .env
    secrets:
      # Give this service access to the runtime secret defined below.
      - app_token

secrets:
  app_token:
    # Read secrets/app_token beside dev.compose.yml on the staging `ssh` target.
    # Mount it at /run/secrets/app_token for our application to read.
    # This staging runtime token is separate from production's token and the shared build PAT.
    file: ./secrets/app_token
```

```dotenv
# /home/runner/staging/my-app/.env on the staging `ssh` target.
# dev.compose.yml injects these application runtime variables through env_file.
APP_ENV=staging
# Our application's listening port, matching dev.compose.yml's container port.
PORT=5000
```

### Production: Production VPS As The `ssh` Target With Docker

The production workflow executes on the runner VPS to build and publish, then connects to the production `ssh` target on the production VPS, where Docker runs the application.

Both workflows run on the runner VPS and share its registry password and private build file. Their `ssh` targets have separate `ssh` keys, `known_hosts` files, runtime variables, and runtime secrets. Those choices are explicit inputs to our application's workflows.

```yaml
# my-repo/.github/workflows/production.yml
# GitHub selects this workflow by branch; Daggerer receives the explicit inputs below.
name: Deploy production

# Main/master pushes update the production deployment using latest.
on:
  push:
    # Only main/master branch pushes invoke this production release.
    branches: [master, main]

permissions:
  contents: read

jobs:
  release:
    # Build on the same runner VPS; --ssh-target deploys to the separate production VPS.
    runs-on: [self-hosted, linux, x64]
    env:
      # Same application name as the my-app paths on both VPSs.
      APPLICATION_NAME: my-app
    steps:
      # Place the triggering main/master commit's repository files in the runner workspace.
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Release to production
        env:
          # Our application's public production build override; not a runtime setting.
          BUILD_VALUES: |
            FOO=production
          # Our application's deployment variable, explicitly matching production's latest tag.
          # Setting --tag does not create or change this variable.
          DEPLOY_VALUES: |
            APP_IMAGE=registry.example.com/team/${{ env.APPLICATION_NAME }}:latest
        run: |
          dagger -W github.com/ninesl/daggerer@master api call release
            # Supply the step's current directory as the Docker build context.
            # Here it contains the main/master commit's files placed by actions/checkout.
            --source=.
            # Select myapp.Dockerfile relative to --source.
            # Daggerer builds the image using this file's instructions; staging selects it too.
            --dockerfile=myapp.Dockerfile
            # Public build defaults from the application checkout.
            --build-env-file=build.env
            # Our hardcoded production override takes precedence over build.env.
            --build-values="$BUILD_VALUES"
            # The same runner VPS file used by staging, containing our dependency PAT.
            --build-secret-env="file://$HOME/secrets-actions/$APPLICATION_NAME/private.env"
            # Same registry and namespace used by staging.
            --registry=registry.example.com/team
            # Produces registry.example.com/team/my-app:latest.
            --app-name="$APPLICATION_NAME"
            # Publish and deploy latest.
            # This controls the image published and pulled, not variables in prod.compose.yml.
            --tag=latest
            # Registry account used for publish and deployment.
            --registry-username=registry-user
            # Registry password file on the runner VPS, used to publish and log in on the production `ssh` target.
            --registry-password="file://$HOME/secrets-actions/$APPLICATION_NAME/registry_password"
            # Production `ssh` target on the production VPS, using the deploy user.
            --ssh-target=deploy@prod.example.com
            # Production `ssh` key whose public key is authorized for the production `ssh` target user.
            --ssh-key="file://$HOME/secrets-actions/$APPLICATION_NAME/production_ssh_key"
            # `known_hosts` file containing the verified host key for the production `ssh` target.
            --known-hosts="file://$HOME/secrets-actions/$APPLICATION_NAME/production_known_hosts"
            # Relative to the production `ssh` target user's home, not the runner's home.
            # Selects /home/deploy/prod/my-app on the production `ssh` target.
            --deploy-directory="prod/$APPLICATION_NAME"
            # Public process env defaults from our checkout's deploy.env; parsed by Dagger.
            # Separate from prod.env read by prod.compose.yml on the production `ssh` target.
            --deploy-env-file=deploy.env
            # Forward our APP_IMAGE to docker compose -f prod.compose.yml up.
            # Overrides matching deploy.env entries; no application variable names are automatic.
            --deploy-values="$DEPLOY_VALUES"
            # Select prod.compose.yml inside --deploy-directory.
            # Its env_file and secrets entries select production's prod.env and secrets/app_token.
            --compose-file=prod.compose.yml
            # Run docker login, docker pull, and docker compose -f prod.compose.yml up.
            --deploy-container-runtime=docker
```

All optional inputs are supplied above. If omitted:

- `--dockerfile`: uses `Dockerfile` instead of `myapp.Dockerfile`.
- `--tag`: uses `latest`, the same tag selected here.
- `--compose-file`: uses `compose.yml` instead of `prod.compose.yml`.
- `--build-env-file`: no public build file is read; explicit build values still apply.
- `--build-values`: no overrides; the public build file's values still apply.
- `--build-secret-env`: no private build file is mounted; our Dockerfile requires it.
- `--deploy-env-file`: no public deployment file is read; explicit deployment values still apply.
- `--deploy-values`: no overrides; the deployment file's values still apply. Our example would need another source for `APP_IMAGE`.

Daggerer does not auto-discover these input files. The runtime `prod.env` and `secrets/app_token` are selected separately by `prod.compose.yml` on production.

```text
# Production `ssh` target: runtime files are separate from the runner VPS's build credentials.
/home/deploy/
└── prod/
    └── my-app/
        ├── prod.compose.yml
        ├── prod.env
        └── secrets/
            └── app_token
```

```yaml
# /home/deploy/prod/my-app/prod.compose.yml on the production `ssh` target.
services:
  app:
    # docker compose -f prod.compose.yml substitutes APP_IMAGE; missing/empty values fail.
    # Our workflow passes it through --deploy-values with the latest tag.
    # APP_IMAGE is our application's convention; any caller can supply it without Daggerer.
    # It selects an image, not a build argument or application environment variable.
    image: ${APP_IMAGE:?APP_IMAGE is required}
    # Restart unless explicitly stopped.
    restart: unless-stopped
    ports:
      # Publish production port 5000 to our application's listening port 5000.
      - "5000:5000"
    env_file:
      # Read prod.env beside prod.compose.yml and inject its values into the application container.
      - prod.env
    secrets:
      # Give this service access to the runtime secret defined below.
      - app_token

secrets:
  app_token:
    # Read secrets/app_token beside prod.compose.yml on the production `ssh` target.
    # Mount it at /run/secrets/app_token for our application to read.
    # This production runtime token is separate from staging's token and the shared build PAT.
    file: ./secrets/app_token
```

```dotenv
# /home/deploy/prod/my-app/prod.env on the production `ssh` target.
# prod.compose.yml injects these application runtime variables through env_file.
APP_ENV=production
# Our application's listening port, matching prod.compose.yml's container port.
PORT=5000
```

## Build Without Deployment

The same inputs also work with `build-only`, without publishing or deploying:

```yaml
# my-repo/.github/workflows/build.yml
# Build step excerpt, after checkout in a self-hosted runner job.
- name: Build our sample application
  env:
    # Hardcoded public overrides for our application's staging build.
    # Keep credentials in the private build file, never in these public values.
    # GitHub Actions uses env:, not env_file:; --build-values passes these explicitly.
    BUILD_VALUES: |
      FOO=staging
      APP_PACKAGE=.
  # build-only has no deployment or registry inputs because it only builds the image.
  run: |
    dagger -W github.com/ninesl/daggerer@master api call build-only
      # Supply the step's current directory as the Docker build context.
      # This example assumes actions/checkout has placed our application's files there.
      --source=.
      # Select our application's myapp.Dockerfile relative to --source, as both release workflows do.
      --dockerfile=myapp.Dockerfile
      # Dagger parses our caller-selected public file into Dockerfile build arguments.
      --build-env-file=build.env
      # Explicit public overrides from the runner step; FOO overrides build.env's hello.
      --build-values="$BUILD_VALUES"
      # Our private build file containing GITHUB_PAT; its filename and path are our choice.
      --build-secret-env=file://$HOME/secrets-actions/my-app/private.env
```

All optional build inputs are supplied above. If omitted:

- `--dockerfile`: uses `Dockerfile` instead of `myapp.Dockerfile`.
- `--build-env-file`: no public build file is read; explicit build values still apply.
- `--build-values`: no overrides; the public build file's values still apply.
- `--build-secret-env`: no private build file is mounted; our Dockerfile requires it.

## GitHub Actions Secrets As Environment Inputs

Alternative credential inputs for our release workflows:

```yaml
# my-repo/.github/workflows/staging.yml
# Credential excerpt: replace the file:// arguments in either annotated release workflow.
# Other build/deployment arguments remain the ones chosen by that workflow.
- name: Release our sample application using GitHub Actions Secrets
  env:
    # Registry password configured in GitHub Actions Secrets.
    REGISTRY_PASSWORD: ${{ secrets.REGISTRY_PASSWORD }}
    # Staging `ssh` key configured in GitHub; production selects its production key instead.
    SSH_KEY: ${{ secrets.STAGING_SSH_KEY }}
    # Verified host key for the staging `ssh` target; production selects its own value.
    KNOWN_HOSTS: ${{ secrets.STAGING_KNOWN_HOSTS }}
    # Entire private build file contents, including GITHUB_PAT, configured in GitHub.
    BUILD_PRIVATE_ENV: ${{ secrets.BUILD_PRIVATE_ENV }}
  run: |
    dagger -W github.com/ninesl/daggerer@master api call release
      # Load the registry password from the step environment as a Dagger Secret.
      --registry-password=env://REGISTRY_PASSWORD
      # env:// loads the step's staging `ssh` key as a Dagger Secret.
      --ssh-key=env://SSH_KEY
      # Strict host-key verification for the staging `ssh` target.
      --known-hosts=env://KNOWN_HOSTS
      # Mount our private dotenv contents as the build_env secret.
      --build-secret-env=env://BUILD_PRIVATE_ENV
```

Arguments absent from this credential excerpt remain as shown in the full staging or production workflow; their defaults and optional-input behavior are described below those workflows.

## How Deployment Works

`deploy` uses the supplied credentials to execute these commands on the `ssh` target:

```text
# Remote deployment operations; runtime and filename come from API inputs.
1. Verify <deploy-directory>/<compose-file> exists.
2. Run docker|podman login with the registry password on stdin.
3. Pull <registry>/<app-name>:<tag>.
4. Forward caller-supplied deployment env values and run <runtime> compose -f <compose-file> up.
```

The resulting path is:

```text
# Execution path for our application: build/publish in Dagger, runtime commands on the `ssh` target.
self-hosted runner
    -> Dagger Engine
        -> `ssh` target
            -> podman compose -f dev.compose.yml up (staging)
               or docker compose -f prod.compose.yml up (production)
```

Strict `ssh` host-key checking uses the supplied `--known-hosts` Secret. The `ssh` target must be reachable from Dagger's container network even when it is the runner VPS itself.

## Caching

- `build-only` uses [`DockerBuild`][dagger-build] to reuse eligible cached work for unchanged inputs.
- `build` and `release` repeat registry authentication and [`Publish`][dagger-publish]; existing image blobs may be reused.
- `deploy` and `release` repeat the remote file check, registry login, image pull, and selected `docker compose` or `podman compose` command.

## Workspace Usage

Load Daggerer directly from Git:

```bash
# Load Daggerer from Git and inspect release parameters without installing a workspace module.
dagger -W github.com/ninesl/daggerer@master api call release --help
```

The consumer needs no `dagger.toml` or `dagger.lock`. To install Daggerer in a consumer workspace instead:

```bash
# Initialize a consumer workspace.
dagger init
# Install Daggerer under the daggerer namespace.
dagger module install github.com/ninesl/daggerer@master
# Call the installed module; -W calls the selected workspace's entrypoint directly instead.
dagger api call daggerer release --help
```

[dagger-build]: https://docs.dagger.io/reference/api/directory#dockerBuild
[dagger-container]: https://docs.dagger.io/reference/api/container
[dagger-publish]: https://docs.dagger.io/reference/api/container#publish
[dagger-secret]: https://docs.dagger.io/reference/api/secret
