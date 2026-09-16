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
- `--ssh-key` authenticates the SSH user selected by `--ssh-target`.
- `--known-hosts` supplies the verified host key used for strict SSH host checking.

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

This workaround may be deprecated or removed when DockerBuild accepts native `{id, secret}` inputs. Track [dagger/dagger#7358](https://github.com/dagger/dagger/issues/7358), [dagger/dagger#9437](https://github.com/dagger/dagger/issues/9437), and [PR #8058](https://github.com/dagger/dagger/pull/8058).

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

Daggerer validates the original Secret, then streams its values over SSH stdin to the remote Compose process. The values do not return through the Dagger API, become command-line arguments, or get written to the checkout or `--ssh-target`.

The file selected by `--compose-file` decides which forwarded values enter the running container. Both example files require the same application variables:

```yaml
environment:
  DATABASE_URL: ${DATABASE_URL:?DATABASE_URL is required}
  APP_SECRET: ${APP_SECRET:?APP_SECRET is required}
```

Users with sufficient Docker or Podman access can inspect a container's runtime environment. Daggerer also cannot prevent application, Dockerfile, or Compose logic from disclosing a value it receives.

## Public Values

`--build-env-file`, `--build-values`, `--deploy-env-file`, and `--deploy-values` are public inputs. Never put credentials in them.

Daggerer merges each public pair in this order:

1. Load `--build-env-file` or `--deploy-env-file`, when supplied.
2. Apply `--build-values` or `--deploy-values` over matching names.
3. Keep values that exist only in the file or only in the explicit input.

Before deployment, Daggerer rejects any public deployment name also present in `--deploy-secret-env-file`. The collision error includes the variable name, never its value.
