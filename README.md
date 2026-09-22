**Daggerer** exposes Dagger API functions to build a `Dockerfile`, publish its image to an OCI registry, deploy image over `ssh` with `docker compose` or `podman compose` [using Dagger](https://dagger.io).

Daggerer allows for external targets for all sources, with support for both [build and deploy secrets](https://docs.dagger.io/reference/api/secret).

Like all things Dagger, Daggerer is intended to be reused and incorporated for other DAGs and repeated across workflows and projects where you want to use the Dagger Engine.

## Quick Reference

Daggerer exposes four API functions:

- `build-dockerfile` builds the file selected by `--dockerfile` (`Dockerfile` by default) with [`Directory.DockerBuild`][dagger-build] and returns a cached [`Container`][dagger-container].
- `build` validates registry authentication, calls `build-dockerfile`, applies [`WithRegistryAuth`](https://docs.dagger.io/reference/api/container#withRegistryAuth), and [`Publish`](https://docs.dagger.io/reference/api/container#publish) publishes the image.
- `deploy` connects to `--ssh-target` on `--ssh-target-port` (`22` by default), pulls an existing image, and uses the runtime selected by `--deploy-container-runtime` to run `--compose-file` (`compose.yml` by default).
- `release` calls `build` and then `deploy` with the same `--registry`, `--app-name`, and `--tag`, simplifying the top-level API.

```bash
# List the available Daggerer functions.
dagger -W github.com/ninesl/daggerer@master api functions
# Inspect each function's parameters and defaults.
dagger -W github.com/ninesl/daggerer@master api call build-dockerfile --help
dagger -W github.com/ninesl/daggerer@master api call build --help
dagger -W github.com/ninesl/daggerer@master api call deploy --help
dagger -W github.com/ninesl/daggerer@master api call release --help

# The command shape is always:
dagger -W github.com/ninesl/daggerer@master api call function --arguments
```

## Contents

> Read through the documentation before adapting an example. The examples favor [grug-brained](https://grugbrain.dev/) development and are explicitly simplified.

- [Quick Reference](#quick-reference)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Staging And Production](#staging-and-production)
- [Secrets](SECRETS.md)

The following examples describe our sample application, `my-app`. We are using Daggerer with our GitHub Action workflow on a self-hosted runner. Use this flowchart as our simple mental model:

```bash
GitHub runner Workflow # uses `daggerer release`
      |
Dagger Engine
      |
[
1. build image
2. publish to registry/image:tagged OCI registry
3. ssh to VPS
4. from VPS, docker compose up registry/image:tagged
]
```

## Prerequisites

`deploy` and `release` require either:

- [`docker`](https://docs.docker.com/engine/install/) and [`docker compose`](https://docs.docker.com/compose/install/linux/) 

or 

- [`podman`](https://podman.io/docs/installation) and [`podman compose`](https://docs.podman.io/en/latest/markdown/podman-compose.1.html) 

installed on `--ssh-target`. This does NOT need to match Dagger's runtime; Dagger only needs to build and publish an OCI image.

Daggerer needs the file selected by `--dockerfile` (`Dockerfile` by default) to build the image.

`build-dockerfile` and `build` **DO NOT** require `docker` or `podman`, they use the self-hosted runner's Dagger Engine directly.

The Dagger CLI can also [select a remote Dagger Engine](https://docs.dagger.io/reference/cli#dagger-engine) with `--engine` or `DAGGER_ENGINE`, including direct connections over `ssh://`, `tcp://`, and `tls://`.

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

The following layout is **not** required by Daggerer, or really recommended. My intention with this example is to highlight how inputs can be sourced from anywhere to be used by Daggerer.

These filesystem paths become Daggerer's API inputs.

See [Deployment Credentials](SECRETS.md#deployment-credentials) for the registry password, `ssh` key, and known-hosts files used below.

Dagger runs `ssh` in a container, so our Podman runner examples use `runner@host.containers.internal`; `known_hosts` must contain that host and the selected port.

```bash
# VPS filesystem
/home/runner/
├── secrets/
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
# /home/runner/apps/my-app/compose.yml on `--ssh-target` (the runner VPS).
services:
  app:
    # docker compose runs the file selected by --compose-file and substitutes APP_IMAGE;
    # missing/empty values fail. APP_IMAGE is unique to our compose.yml and is explicitly
    # passed with --deploy-values below.
    image: ${APP_IMAGE:?APP_IMAGE is required}
    restart: unless-stopped
    ports:
      - "5000:5000"
```

> In actual production you should use a reverse proxy like [`caddy`](https://caddyserver.com/docs/quick-starts/reverse-proxy) or [`nginx`](https://docs.nginx.com/nginx/admin-guide/web-server/reverse-proxy/)

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
      # APPLICATION_NAME supplies --app-name and the my-app path segments below.
      APPLICATION_NAME: my-app
    steps:
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Build, publish, and deploy
        env:
          # Our application's --deploy-values input; this is the image reference we publish.
          # Daggerer forwards these public values to --compose-file on --ssh-target.
          DEPLOY_VALUES: |
            APP_IMAGE=registry.example.com/team/${{ env.APPLICATION_NAME }}:latest
        run: dagger -W github.com/ninesl/daggerer@master api call release 
            # Supply the step's current directory as the --source build context.
            # . contains the repository files placed by actions/checkout
            --source=.

            # --registry supplies the registry host and image namespace.
            --registry=registry.example.com/team

            # --registry, --app-name, and --tag form registry.example.com/team/my-app:latest.
            --app-name="$APPLICATION_NAME"

            # --registry-username is used for publishing and remote registry login.
            --registry-username=registry-user

            # --registry-password loads the runner-local file as a Dagger Secret.
            --registry-password="file://$HOME/secrets/$APPLICATION_NAME/registry_password"

            # Dagger's SSH container reaches its own Podman host through this name.
            --ssh-target="runner@host.containers.internal"

            # Omitted --ssh-target-port defaults to 22. Set it for a nonstandard SSH port.

            # --ssh-key loads the key preauthorized for the --ssh-target user.
            --ssh-key="file://$HOME/secrets/$APPLICATION_NAME/ssh_key"

            # --known-hosts loads the verified host key for --ssh-target.
            --known-hosts="file://$HOME/secrets/$APPLICATION_NAME/known_hosts"

            # --deploy-container-runtime selects docker login, pull, and compose.
            # release currently supports only `docker` or `podman`.
            --deploy-container-runtime=docker

            # Relative to the --ssh-target user's home, not the runner's checkout.
            # --deploy-directory selects /home/runner/apps/my-app on this --ssh-target.
            --deploy-directory="apps/$APPLICATION_NAME"

            # --deploy-values forwards our variables to --compose-file on --ssh-target.
            # APP_IMAGE is used for our unique `compose.yml` specific to my-app
            # This variable only exists here because we pass it; Daggerer does not require it
            --deploy-values="$DEPLOY_VALUES"
```

- Omitted `--dockerfile`: uses default `Dockerfile` at the root of `--source`.
- Omitted `--tag`: uses default `latest`.
- Omitted `--compose-file`: uses default `compose.yml`.

```bash
# What the final deployment looks like on --ssh-target
# $HOME belongs to the --ssh-target user; --deploy-directory selects /apps/my-app,
# which contains the file selected by --compose-file.
# Our workflow passes the APP_IMAGE required by --compose-file through --deploy-values.
# Recreate the service in the background and remove services no longer in --compose-file.
cd "$HOME/apps/my-app" && \
  APP_IMAGE=registry.example.com/team/my-app:latest \
  docker compose -f compose.yml up -d --force-recreate --remove-orphans
```

Any failed registry, build, publish, `ssh`, pull, or `docker compose -f compose.yml up` step stops this release.

## Staging And Production

This example calls the same Daggerer `release` function from two workflows:

- Staging builds on the self-hosted runner and runs `podman compose` on that VPS.
- Production builds on the same runner and runs `docker compose` on a separate VPS.

Our application uses a different host port, public `APP_ENV`, private `DATABASE_URL`, and application secret in each environment. Those requirements belong to our application, not Daggerer. Each workflow uses Daggerer's public-value precedence to replace the checkout's `APP_ENV=DEV`, while `--deploy-secret-env-file` separately supplies private values.

### Filesystem

```text
# Self-hosted runner
/home/runner/
├── secrets/my-app/
│   ├── registry_password
│   ├── github_pat
│   ├── staging.env
│   ├── production.env
│   ├── staging_ssh_key
│   ├── staging_known_hosts
│   ├── production_ssh_key
│   └── production_known_hosts
└── actions-runner/my-app/

# Staging --ssh-target on the runner VPS
/home/stageuser/
└── staging/my-app/
    └── staging.compose.yml

# Production --ssh-target
/home/deployuser/
└── apps/my-app/
    └── production.compose.yml

# Application repository checked out by GitHub Actions
my-repo/
├── .github/workflows/
│   ├── staging.yml
│   └── production.yml
├── .env                    # shared public runtime defaults
├── Dockerfile
└── .dockerignore
```

The files under `/home/runner/secrets/my-app` are in the self-hosted runner. [Secrets](SECRETS.md) explains what each file contains and how Daggerer handles it in the example.

The checked-out `.env` contains a public development default shared by both workflows:

```dotenv
# my-repo/.env
APP_ENV=DEV
```

### Staging

Branches other than `main` and `master` pass the exact `${{ github.sha }}` to `--tag`, then deploy that image to staging with `podman compose`.

#### Staging Target YAML

```yaml
# /home/stageuser/staging/my-app/staging.compose.yml on the staging --ssh-target.
services:
  app:
    # --deploy-values supplies the commit image published by this workflow.
    image: ${APP_IMAGE:?APP_IMAGE is required}
    restart: unless-stopped
    ports:
      # staging.compose.yml publishes staging on host port 5001.
      - "5001:5000"
    environment:
      # .env starts at DEV; --deploy-values overwrites it with STAGING.
      APP_ENV: ${APP_ENV:?APP_ENV is required}
      # staging.env separately supplies our database and application secret.
      DATABASE_URL: ${DATABASE_URL:?DATABASE_URL is required}
      STAGING_SECRET_KEY: ${STAGING_SECRET_KEY:?STAGING_SECRET_KEY is required}
```

Our `staging.compose.yml` uses the staging port and application variables. See [Deployment Environment Secrets](SECRETS.md#deployment-environment-secrets) for `staging.env`.

#### Staging Workflow

The PAT is described in [Dockerfile Build Secrets](SECRETS.md#dockerfile-build-secrets). Registry and `ssh` files are described in [Deployment Credentials](SECRETS.md#deployment-credentials).

```yaml
# .github/workflows/staging.yml
name: Deploy staging

on:
  push:
    branches-ignore: [main, master]

permissions:
  contents: read

jobs:
  release:
    runs-on: [self-hosted, linux, x64]
    env:
      # Used by --app-name and our runner-local file paths.
      APPLICATION_NAME: my-app
      # The same SHA used by --tag below.
      DEPLOY_IMAGE: registry.example.com/team/my-app:${{ github.sha }}
    steps:
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Build, publish, and deploy staging
        env:
          # Public values created in memory from this workflow.
          # APP_ENV overwrites the DEV value in the checked-out .env.
          DEPLOY_VALUES: |
            APP_ENV=STAGING
            APP_IMAGE=${{ env.DEPLOY_IMAGE }}
        run: dagger -W github.com/ninesl/daggerer@master api call \
            # Map our runner-local PAT to the Dockerfile's github_pat secret ID.
            with-build-secret \
            --id=github_pat \
            --secret="file://$HOME/secrets/$APPLICATION_NAME/github_pat" \

            # Build, publish, and deploy this checkout.
            release \
            --source=. \
            --registry=registry.example.com/team \
            --app-name="$APPLICATION_NAME" \

            # Use the triggering commit SHA as the exact image tag.
            --tag=${{ github.sha }} \

            # Authenticate the publish and staging pull.
            --registry-username=registry-user \
            --registry-password="file://$HOME/secrets/$APPLICATION_NAME/registry_password" \

            # Connect from Dagger's SSH container back to its own Podman host.
            --ssh-target="stageuser@host.containers.internal" \
            --ssh-key="file://$HOME/secrets/$APPLICATION_NAME/staging_ssh_key" \
            --known-hosts="file://$HOME/secrets/$APPLICATION_NAME/staging_known_hosts" \

            # Run podman compose on the staging --ssh-target.
            --deploy-container-runtime=podman \

            # Select /home/stageuser/staging/my-app on the staging --ssh-target.
            --deploy-directory="staging/$APPLICATION_NAME" \

            # Run staging.compose.yml from --deploy-directory.
            --compose-file=staging.compose.yml \

            # Load our checked-out public runtime defaults, including APP_ENV=DEV.
            --deploy-env-file=.env \

            # Override APP_ENV with STAGING and pass the commit image selected by --tag.
            --deploy-values="$DEPLOY_VALUES" \

            # Pass our staging DATABASE_URL and STAGING_SECRET_KEY over ssh stdin.
            --deploy-secret-env-file="file://$HOME/secrets/$APPLICATION_NAME/staging.env"
```

### Production

Pushes to `main` or `master` omit `--tag`, so Daggerer publishes and deploys the default `latest` image with `docker compose`.

#### Production Target YAML

```yaml
# /home/deployuser/apps/my-app/production.compose.yml on the production --ssh-target.
services:
  app:
    # --deploy-values supplies the latest image published by this workflow.
    image: ${APP_IMAGE:?APP_IMAGE is required}
    restart: unless-stopped
    ports:
      # production.compose.yml publishes production on host port 5000.
      - "5000:5000"
    environment:
      # .env starts at DEV; --deploy-values overwrites it with PRODUCTION.
      APP_ENV: ${APP_ENV:?APP_ENV is required}
      # production.env separately supplies our database and application secret.
      DATABASE_URL: ${DATABASE_URL:?DATABASE_URL is required}
      PROD_SECRET_TOKEN: ${PROD_SECRET_TOKEN:?PROD_SECRET_TOKEN is required}
```

Our `production.compose.yml` uses the production port and application variables. See [Deployment Environment Secrets](SECRETS.md#deployment-environment-secrets) for `production.env`.

#### Production Workflow

Production uses the same [Dockerfile Build Secret](SECRETS.md#dockerfile-build-secrets), plus the production files from [Deployment Credentials](SECRETS.md#deployment-credentials).

```yaml
# .github/workflows/production.yml
name: Deploy production

on:
  push:
    branches: [main, master]

permissions:
  contents: read

jobs:
  release:
    runs-on: [self-hosted, linux, x64]
    env:
      # Used by --app-name and our runner-local file paths.
      APPLICATION_NAME: my-app
      # Match the default latest tag used when --tag is omitted.
      DEPLOY_IMAGE: registry.example.com/team/my-app:latest
    steps:
      - uses: actions/checkout@v5
        with:
          persist-credentials: false

      - name: Build, publish, and deploy production
        env:
          # Public values created in memory from this workflow.
          # APP_ENV overwrites the DEV value in the checked-out .env.
          DEPLOY_VALUES: |
            APP_ENV=PRODUCTION
            APP_IMAGE=${{ env.DEPLOY_IMAGE }}
        run: dagger -W github.com/ninesl/daggerer@master api call \
            # Map our runner-local PAT to the Dockerfile's github_pat secret ID.
            with-build-secret \
            --id=github_pat \
            --secret="file://$HOME/secrets/$APPLICATION_NAME/github_pat" \

            # Build, publish, and deploy this checkout.
            release \
            --source=. \
            --registry=registry.example.com/team \
            --app-name="$APPLICATION_NAME" \

            # Authenticate the publish and production pull.
            --registry-username=registry-user \
            --registry-password="file://$HOME/secrets/$APPLICATION_NAME/registry_password" \

            # Connect to the production VPS with production credentials.
            --ssh-target="deployuser@prod.example.com" \
            --ssh-key="file://$HOME/secrets/$APPLICATION_NAME/production_ssh_key" \
            --known-hosts="file://$HOME/secrets/$APPLICATION_NAME/production_known_hosts" \

            # Run docker compose on the production --ssh-target.
            --deploy-container-runtime=docker \

            # Select /home/deployuser/apps/my-app on the production --ssh-target.
            --deploy-directory="apps/$APPLICATION_NAME" \

            # Run production.compose.yml from --deploy-directory.
            --compose-file=production.compose.yml \

            # Load our checked-out public runtime defaults, including APP_ENV=DEV.
            --deploy-env-file=.env \

            # Override APP_ENV with PRODUCTION and pass the default latest image.
            --deploy-values="$DEPLOY_VALUES" \

            # Pass our production DATABASE_URL and PROD_SECRET_TOKEN over ssh stdin.
            --deploy-secret-env-file="file://$HOME/secrets/$APPLICATION_NAME/production.env"
```

Any failed registry, build, publish, `ssh`, pull, `podman compose`, or `docker compose` step stops its release. Daggerer only uses the files and values passed by each workflow.

### Self-hosted runner with a co-located registry

If a rootless Podman Dagger Engine and OCI registry share a host, map the registry hostname to the Podman host. Without this mapping, `Container.Publish` may fail with `connection refused`.

```bash
podman run -d \
  --name dagger-engine \
  --privileged \
  --network pasta \
  --add-host registry.example.com:host-gateway \
  --restart always \
  --volume dagger-engine:/var/lib/dagger \
  registry.dagger.io/engine:v1.0.0-beta.13
```

Select that engine directly in the workflow:

```bash
dagger \
  --engine=container+podman://dagger-engine \
  -W github.com/ninesl/daggerer@master \
  api call release ...
```

Keep the public registry hostname for `--registry` so deployment hosts pull the same image. The mapping is only needed while the registry and Dagger Engine share a host.

I'm unsure if this behavior is the same with `docker`, I'm assuming yes as this is dagger specific-behavior.

[dagger-build]: https://docs.dagger.io/reference/api/directory#dockerBuild
[dagger-container]: https://docs.dagger.io/reference/api/container
