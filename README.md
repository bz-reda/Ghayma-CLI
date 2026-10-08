# Ghayma CLI

Command-line tool for deploying and managing applications on [Ghayma](https://dash.ghayma.cloud).

## Installation

**macOS / Linux**

```bash
curl -fsSL https://dash.ghayma.cloud/install.sh | sh
```

**Windows** (PowerShell)

```powershell
irm https://dash.ghayma.cloud/install.ps1 | iex
```

Or download a binary for your platform from [Releases](https://github.com/bz-reda/Ghayma-CLI/releases).

## Quick Start

```bash
ghayma register          # Create an account
ghayma login              # Authenticate
ghayma init               # Initialize a project
ghayma deploy             # Deploy (production when the target site is a production site)
```

## Commands

### Account

| Command | Description |
|---|---|
| `ghayma register` | Create a new Ghayma account |
| `ghayma login` | Authenticate with Ghayma (opens browser) |
| `ghayma login --email` | Authenticate with email/password |
| `ghayma logout` | Log out and clear saved credentials |
| `ghayma whoami` | Show the current CLI identity |
| `ghayma version` | Show CLI version |

### API tokens

Account API tokens act as you across the API: scoped, optionally restricted to projects. A token is shown once, at `create` and at `rotate`, and is named by its id, its prefix (`gh_xxxxxxx`) or its name. A token can only create, rotate or revoke tokens no wider than itself.

| Command | Description |
|---|---|
| `ghayma token create <name> [--scope deploy,databases] [--expires 90] [--project <slug> ...] [--json]` | Create a token (default scope `deploy`, 90 days; `--expires 0` = never) |
| `ghayma token list [--all] [--json]` | List tokens; `--all` includes revoked ones |
| `ghayma token revoke <id\|prefix\|name> [--yes]` | Revoke a token; revoking the CLI's own token logs it out |
| `ghayma token rotate <id\|prefix\|name> [--expires <days>] [--json]` | Replace a token's secret. By default the new token keeps the remaining lifetime of the old one; `--expires N` sets a new one. The CLI's own token is updated in its config |

#### Two logins side by side

`GHAYMA_CONFIG` points the CLI at another config file, so a second login lives
beside the first instead of replacing it:

```bash
GHAYMA_CONFIG=~/.ghayma-staging.json ghayma login --host https://api.staging.ghayma.tech --email
GHAYMA_CONFIG=~/.ghayma-staging.json ghayma whoami
```

`GHAYMA_API_HOST` overrides the host for a single command without touching any file.

### Projects

| Command | Description |
|---|---|
| `ghayma init` | Initialize a new project in the current directory |
| `ghayma init --plan <slug>` | Initialize with a specific plan (e.g. `hobby`, `pro`); interactive plan picker when omitted |
| `ghayma link` | Link the current directory to an existing project |
| `ghayma project transfer` | Transfer project ownership (also: `project status`, `project cancel`, `project accept`) |
| `ghayma deploy` | Deploy the current project — production when the target site is a production site |
| `ghayma deploy --prod` | Accepted, but no longer changes anything: the target site's environment decides |
| `ghayma deploy --no-connect` | Deploy without connecting the services no site uses yet; prints the `ghayma connect` command for each. Without it, a terminal deploy offers each one to the site it deploys. The offer and `--no-connect` apply to source deploys only, not to `--image` |
| `ghayma promote --from <site>` | Ship the image a site already runs onto another site (default target: the project's default site) |
| `ghayma deploy --image <tag>` | Deploy an image already pushed with `ghayma docker push` instead of uploading source |
| `ghayma status` | List your projects |
| `ghayma logs` | View application logs |
| `ghayma logs -n 500` | View last 500 lines |
| `ghayma rollback` | Rollback to a previous deployment |
| `ghayma delete` | Delete the current project and all its resources |

### Docker Images

Build an image locally and push it straight to your site's repository in the Ghayma registry — only the layers the registry does not already have are uploaded — then deploy it. Requires the docker CLI.

```bash
docker build -t my-app .
ghayma docker push my-app --tag v1
ghayma deploy --image v1
```

| Command | Description |
|---|---|
| `ghayma docker push <image[:tag]>` | Push a locally built image to this site's repository |
| `ghayma docker push <image> --tag v1` | Push it under a different tag (default: the local image's tag, else `latest`) |
| `ghayma docker push <image> --site admin` | Push to another site's repository |
| `ghayma docker push <image> --deploy [--prod]` | Push and deploy it in one command |
| `ghayma deploy --image <tag\|sha256:digest>` | Deploy an already-pushed image |

The image must expose a `linux/amd64` variant, declare a numeric non-root `USER`, and listen on `$PORT` — the platform refuses it at deploy time otherwise, and says which rule it broke in the deployment's build log.

### Points

Each project has a points budget (or runs pay-as-you-go). Databases, apps, storage, and auth apps each consume points based on their tier and size.

| Command | Description |
|---|---|
| `ghayma points` | Show the project's points meter and per-resource breakdown |

### Sites

| Command | Description |
|---|---|
| `ghayma site list` | List all sites in the current project |
| `ghayma site create [name]` | Add a new site to the current project |
| `ghayma site create <name> --env <kind>` | Add a site as a `development` (default), `staging` or `production` environment |
| `ghayma site environment <site> <kind>` | Change which environment a site is (the default site is always production) |
| `ghayma site inherit-env <site> <on\|off>` | Resolve this site's variables from the default site's, overridden by its own |
| `ghayma site use <slug>` | Switch the active site for the project |
| `ghayma site scale --tier <tier>` | Scale the active app to a new compute tier (e.g. `a`, `b`, `c`, `d`) |
| `ghayma site scale --replicas <n>` | Scale the active app to `n` replicas (must be >= 1) |
| `ghayma site scale --site <slug> --tier <tier> --replicas <n>` | Scale a specific site by tier and/or replica count |

### Domains

| Command | Description |
|---|---|
| `ghayma domain create [domain]` | Add a custom domain to the current project |
| `ghayma domain list` | List domains for the current project |
| `ghayma domain delete [domain]` | Remove a domain from the current project |

### Environment Variables

| Command | Description |
|---|---|
| `ghayma env set KEY=VALUE` | Set environment variables |
| `ghayma env set --file .env.production` | Set from file |
| `ghayma env list` | List environment variables |
| `ghayma env delete KEY` | Remove an environment variable |
| `ghayma env import <file>` | Import variables from a dotenv file |
| `ghayma env pull` | Write the site's effective variables (connection-derived ones included) to a git-ignored `.env.local` |

### Connections

A connection lets one site (app) use one of the project's services — a database, a bucket or an auth app — at a level. It is what hands the app its variables, opens the network path to a database and shapes the app's managed platform key.

| Command | Description |
|---|---|
| `ghayma connections [--site <slug>] [--json]` | List which apps may use which services |
| `ghayma connect <database\|bucket\|auth> <name> [--site <slug>] [--level <level>]` | Connect a service to an app (levels: database `connect` or `read-only`, bucket `read-write` or `read`, auth `client` or `admin`) |
| `ghayma disconnect <database\|bucket\|auth> <name> [--site <slug>] [--yes]` | Disconnect a service from an app |
| `ghayma connect --local [--site <slug>] [--out <file>]` | Tunnel the app's databases to localhost and write `.env.local` pointing at them |

### External access

An external principal is a named identity OUTSIDE Ghayma — a BI tool, a partner's CI — that holds its own credential for one database or bucket, with an optional source-IP allowlist and an optional expiry. Its credential is shown once, at `add` and at `rotate`. Auth apps have no principals: their external access is their restricted project keys, minted in the console.

| Command | Description |
|---|---|
| `ghayma access <database\|bucket> <name> [--json]` | Show the apps connected to a service and the external principals that reach it |
| `ghayma access add <database\|bucket> <name> --name <principal> [--level <level>] [--allow <cidr,...>] [--expires <days>]` | Grant an outside principal its own credential (printed once) |
| `ghayma access rotate <database\|bucket> <name> <principal> [--yes]` | Replace a principal's secret, keeping its identity (printed once) |
| `ghayma access allow <database\|bucket> <name> <principal> --set <cidr,...>` | Replace the sources it may connect from; `--set ""` clears them |
| `ghayma access revoke <database\|bucket> <name> <principal> [--yes]` | Drop a principal's credential for good |

The allowlist is enforced at the front door, before any authentication, so it cannot tell which principal a connection belongs to: while any principal of a resource is unrestricted, no IP filter is enforced for that resource at all.

### Databases

| Command | Description |
|---|---|
| `ghayma db create [name]` | Create a managed database |
| `ghayma db create [name] --type mongodb` | Create with specific type (postgres, mongodb, valkey) |
| `ghayma db create [name] --type valkey --mode <cache\|store>` | Create a Valkey (Redis-compatible: every Redis client works with it). `cache` (default) evicts the least-recently-used keys when memory is full; `store` never evicts and keeps an append-only file. Connected apps receive `REDIS_URL` and `VALKEY_URL` at runtime (not during builds) |
| `ghayma db create [name] --tier <tier> --disk-gb <gb> --backup <schedule>` | Create with a specific compute tier, disk size, and backup schedule (weekly, daily, sixhourly); interactive pickers appear when you pass none of these |
| `ghayma db create [name] --site <slug>` | Connect the new database to a site; repeat `--site` for several. Without it, a terminal asks about each site |
| `ghayma db create [name] --no-connect` | Connect the new database to no site, without asking |
| `ghayma db resize [name] --tier <tier>` | Change a database's compute tier |
| `ghayma db resize [name] --disk-gb <gb>` | Grow or shrink a database's disk. A grow stays online; a shrink stops the database for about a minute while its data moves to the smaller disk. A Valkey disk can grow but cannot shrink yet. Waits for the change to finish; `--no-wait` returns once it has started |
| `ghayma db resize [name] --backup <schedule>` | Change the backup schedule (weekly, daily, sixhourly) |
| `ghayma db list` | List your databases, with any disk change in progress |
| `ghayma db info [name]` | Show database details, including disk used and, for Postgres and MongoDB, the smallest disk it can shrink to |
| `ghayma db mode [name] <cache\|store>` | Switch a Valkey between `cache` (evicts the least-recently-used keys when memory is full) and `store` (never evicts; keeps an append-only file). Switching restarts the database: a few seconds of downtime |
| `ghayma db logs [name] [-n <lines>] [-f]` | Show a database's engine log (any engine). `-n/--lines` sets how many recent lines (1-1000, default 200); `-f/--follow` keeps printing new lines until Ctrl-C, and the platform ends a follow after 10 minutes |
| `ghayma db credentials [name]` | Show connection credentials. A Valkey has no shared credential: each connected app has its own user in `REDIS_URL` / `VALKEY_URL` |
| `ghayma db stop [name]` | Stop database (preserves data) |
| `ghayma db start [name]` | Start a stopped database |
| `ghayma db rotate [name]` | Rotate database password. A Valkey has no shared password: rotate one app's user with `ghayma connections rotate database <name> --site <site>` |
| `ghayma db delete [name]` | Delete database and all its data |

Reaching a database from outside Ghayma is a named principal with its own credential — see [External access](#external-access) above.

### Storage

| Command | Description |
|---|---|
| `ghayma storage create [name]` | Create a storage bucket |
| `ghayma storage create [name] --quota-gb <gb>` | Create with a specific storage quota in GB; interactive picker when omitted |
| `ghayma storage create [name] --site <slug>` | Connect the new bucket to a site; repeat `--site` for several. Without it, a terminal asks about each site |
| `ghayma storage create [name] --no-connect` | Connect the new bucket to no site, without asking |
| `ghayma storage list` | List your storage buckets |
| `ghayma storage info [name]` | Show bucket details |
| `ghayma storage credentials [name]` | Show S3 access credentials |
| `ghayma storage expose [name]` | Make bucket publicly accessible |
| `ghayma storage unexpose [name]` | Disable public access |
| `ghayma storage rotate [name]` | Rotate S3 access credentials |
| `ghayma storage delete [name]` | Delete bucket and all its data |

### Auth Apps

| Command | Description |
|---|---|
| `ghayma auth create [name]` | Create a managed auth service |
| `ghayma auth create [name] --app-id my-app` | Create with custom app ID |
| `ghayma auth create [name] --users <bracket>` | Set the user-capacity bracket (1k, 10k, 100k, 1m); interactive picker when omitted |
| `ghayma auth create [name] --2fa` | Enable two-factor authentication (authenticator app / TOTP) |
| `ghayma auth create [name] --site <slug>` | Connect the new auth app to a site; repeat `--site` for several. Without it, a terminal asks about each site |
| `ghayma auth create [name] --no-connect` | Connect the new auth app to no site, without asking |
| `ghayma auth list` | List your auth apps |
| `ghayma auth info [name]` | Show auth app details and endpoints |
| `ghayma auth config [name]` | Configure OAuth providers and settings |
| `ghayma auth users [name]` | List users for an auth app |
| `ghayma auth stats [name]` | Show auth app statistics |
| `ghayma auth rotate-keys [name]` | Rotate JWT signing keys |
| `ghayma auth delete [name]` | Delete auth app and all its users |

## Project Configuration

Running `ghayma init` creates a `.ghayma.json` file in the project directory:

```json
{
  "project_id": "uuid",
  "name": "my-app",
  "slug": "my-app",
  "framework": "nextjs",
  "site_id": "uuid",
  "site_name": "main",
  "site_slug": "main"
}
```

The user-level config (auth token, API host) is stored separately, in `~/.paas-cli.json` or wherever `GHAYMA_CONFIG` points.

## Building from Source

```bash
git clone https://github.com/bz-reda/Ghayma-CLI.git
cd Ghayma-CLI
make build
./ghayma version
```

## Documentation

Full documentation at [docs.ghayma.cloud/cli](https://docs.ghayma.cloud/cli).

## License

MIT
