# Secrets

[Back to the Daggerer README](README.md)

Our examples keep build secrets, deployment environment secrets, and infrastructure credentials in separate runner-local files. Each file has one explicit job in the workflow:

```text
# Files read by the self-hosted runner service user.
$HOME/secrets/my-app/
├── registry_password        # --registry-password for publishing and remote registry login
├── github_pat               # with-build-secret --secret for private build dependencies
├── staging.env              # staging --deploy-secret-env-file
├── production.env           # production --deploy-secret-env-file
├── staging_ssh_key          # staging --ssh-key
├── staging_known_hosts      # staging --known-hosts
├── production_ssh_key       # production --ssh-key
└── production_known_hosts   # production --known-hosts
```

These paths are our chosen layout, not names Daggerer discovers automatically. Keep the files outside the application checkout and restrict them to the runner service user:

```bash
chmod 700 "$HOME/secrets" "$HOME/secrets/my-app"
chmod 600 "$HOME/secrets/my-app"/*
```

## Deployment Credentials

The release workflows use three Dagger Secret inputs for infrastructure access:

- `--registry-password` authenticates both the image publish and registry login on `--ssh-target`.
- `--ssh-key` authenticates the `ssh` user selected by `--ssh-target`.
- `--known-hosts` supplies the verified host key used for strict `ssh` host checking.

These inputs accept either `file://` or `env://`. The README uses runner-local `file://` paths so the source of each credential is visible beside the API call.

The build and deployment files below intentionally require `file://`; do not pass their values through public command arguments.

## Dockerfile Build Secrets

`with-build-secret` maps one runner-local file to one Dockerfile secret mount. `--id` names the mount and `--secret` selects the file:

```dockerfile
# The workflow uses with-build-secret --id=github_pat before build or release.
RUN --mount=type=secret,id=github_pat,required=true \
    GITHUB_PAT="$(cat /run/secrets/github_pat)" && \
    use-private-dependency "$GITHUB_PAT"
```

Chain `with-build-secret` once for every secret ID required by the file selected by `--dockerfile`:

```text
dagger -W github.com/ninesl/daggerer@master api call \
  with-build-secret \
  --id=github_pat \
  --secret=file://$HOME/secrets/my-app/github_pat \
  with-build-secret \
  --id=npm_token \
  --secret=file://$HOME/secrets/my-app/npm_token \
  release ...
```

Each `--id` must be unique in the chain and must exactly match a Dockerfile `RUN --mount=type=secret,id=...` ID.

### Dagger Beta Workaround

Dagger `v1.0.0-beta.13` does not let `Directory.DockerBuild` directly pair an arbitrary ID with a `Secret`. Daggerer therefore uses Dagger's documented [`Plaintext()` then `SetSecret()` workaround](https://docs.dagger.io/0.21/cookbook/#use-secret-in-dockerfile-build) while assigning the requested BuildKit ID.

The secret briefly exists in module memory during that assignment. Daggerer does not log, parse, write, or include it in errors. Restrict who can modify or invoke this module because code running inside the module shares that trust boundary.

Native `{id, secret}` DockerBuild support is discussed in [dagger/dagger#7358](https://github.com/dagger/dagger/issues/7358), [dagger/dagger#9437](https://github.com/dagger/dagger/issues/9437), and [dagger/dagger#8058](https://github.com/dagger/dagger/pull/8058).

Keeping the build in a Dockerfile gives up some native Dagger programmability, composition, and direct `WithMountedSecret` handling. Daggerer accepts that tradeoff so an existing Dockerfile can still use Dagger's portable execution, graph caching, observability, publishing, and release orchestration.

## Deployment Environment Secrets

`--deploy-secret-env-file` accepts one private dotenv file through `file://`. Our workflows choose a different file for each environment:

```dotenv
# $HOME/secrets/my-app/staging.env
DATABASE_URL=postgres://app:staging-password@staging-db.internal:5432/my_app
APP_SECRET=replace-with-staging-secret
```

```dotenv
# $HOME/secrets/my-app/production.env
DATABASE_URL=postgres://app:production-password@prod-db.internal:5432/my_app
APP_SECRET=replace-with-production-secret
```

Daggerer validates the original Secret, then streams its values over `ssh` stdin. Staging supplies `staging.env` to `podman compose`; production supplies `production.env` to `docker compose`. The values do not return through the Dagger API, become command-line arguments, or get written to the checkout or `--ssh-target`.

The [`staging.compose.yml` example](README.md#staging-target-yaml) and [`production.compose.yml` example](README.md#production-target-yaml) each choose which forwarded values enter their running container. Both explicitly require `DATABASE_URL` and `APP_SECRET`.

Users with sufficient `podman` or `docker` access can inspect a container's runtime environment. Daggerer also cannot prevent application, Dockerfile, `podman compose`, or `docker compose` logic from disclosing a value it receives.

## Public Values

`--build-env-file`, `--build-values`, `--deploy-env-file`, and `--deploy-values` are public inputs. Never put credentials in them.

Daggerer merges each public pair in this order:

1. Load `--build-env-file` or `--deploy-env-file`, when supplied.
2. Apply `--build-values` or `--deploy-values` over matching names.
3. Keep values that exist only in the file or only in the explicit input.

Before deployment, Daggerer rejects any public deployment name also present in `--deploy-secret-env-file`. The collision error includes the variable name, never its value.
