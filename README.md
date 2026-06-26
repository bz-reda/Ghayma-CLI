# Ghayma CLI

Command-line tool for deploying and managing applications on [Ghayma](https://dashboard.ghayma.dev).

## Installation

**macOS / Linux**

```bash
curl -fsSL https://dash.ghayma.cloud/install.sh | sh
```

**Windows** (PowerShell)

```powershell
irm https://dash.ghayma.cloud/install.ps1 | iex
```

Or download a binary for your platform from [Releases](https://github.com/bz-reda/Espace-Tech-Cloud-CLI/releases).

## Quick Start

```bash
ghayma register          # Create an account
ghayma login              # Authenticate
ghayma init               # Initialize a project
ghayma deploy --prod      # Deploy to production
```

## Commands

### Account

| Command | Description |
|---|---|
| `ghayma register` | Create a new Ghayma account |
| `ghayma login` | Authenticate with Ghayma (opens browser) |
| `ghayma login --email` | Authenticate with email/password |
| `ghayma logout` | Log out and clear saved credentials |
| `ghayma version` | Show CLI version |

### Projects

| Command | Description |
|---|---|
| `ghayma init` | Initialize a new project in the current directory |
| `ghayma deploy` | Deploy the current project (preview) |
| `ghayma deploy --prod` | Deploy to production |
| `ghayma status` | List your projects |
| `ghayma logs` | View application logs |
| `ghayma logs -n 500` | View last 500 lines |
| `ghayma rollback` | Rollback to a previous deployment |
| `ghayma delete` | Delete the current project and all its resources |

### Sites

| Command | Description |
|---|---|
| `ghayma site list` | List all sites in the current project |
| `ghayma site add [name]` | Add a new site to the current project |
| `ghayma site use <slug>` | Switch the active site for the project |

### Domains

| Command | Description |
|---|---|
| `ghayma domain add [domain]` | Add a custom domain to the current project |
| `ghayma domain list` | List domains for the current project |
| `ghayma domain remove [domain]` | Remove a domain from the current project |

### Environment Variables

| Command | Description |
|---|---|
| `ghayma env set KEY=VALUE` | Set environment variables |
| `ghayma env set --file .env.production` | Set from file |
| `ghayma env list` | List environment variables |
| `ghayma env remove KEY` | Remove an environment variable |

### Databases

| Command | Description |
|---|---|
| `ghayma db create [name]` | Create a managed database |
| `ghayma db create [name] --type redis` | Create with specific type (postgres, redis, mongodb) |
| `ghayma db list` | List your databases |
| `ghayma db info [name]` | Show database details |
| `ghayma db credentials [name]` | Show connection credentials |
| `ghayma db link [name] --project [slug]` | Link database to a project (injects env vars) |
| `ghayma db unlink [name]` | Unlink database from its project |
| `ghayma db expose [name]` | Enable external access |
| `ghayma db unexpose [name]` | Disable external access |
| `ghayma db stop [name]` | Stop database (preserves data) |
| `ghayma db start [name]` | Start a stopped database |
| `ghayma db rotate [name]` | Rotate database password |
| `ghayma db delete [name]` | Delete database and all its data |

### Storage

| Command | Description |
|---|---|
| `ghayma storage create [name]` | Create a storage bucket |
| `ghayma storage list` | List your storage buckets |
| `ghayma storage info [name]` | Show bucket details |
| `ghayma storage credentials [name]` | Show S3 access credentials |
| `ghayma storage link [name] --project [slug]` | Link bucket to a project (injects S3 env vars) |
| `ghayma storage unlink [name]` | Unlink bucket from its project |
| `ghayma storage expose [name]` | Make bucket publicly accessible |
| `ghayma storage unexpose [name]` | Disable public access |
| `ghayma storage rotate [name]` | Rotate S3 access credentials |
| `ghayma storage delete [name]` | Delete bucket and all its data |

### Auth Apps

| Command | Description |
|---|---|
| `ghayma auth create [name]` | Create a managed auth service |
| `ghayma auth create [name] --app-id my-app` | Create with custom app ID |
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

New projects use `.ghayma.json`. Existing projects that already have a `.espacetech.json` keep working — the CLI reads it as a fallback when no `.ghayma.json` is present, so no migration is required. The user-level config (auth token, API host) remains at `~/.paas-cli.json`.

## Building from Source

```bash
git clone https://github.com/bz-reda/Espace-Tech-Cloud-CLI.git
cd Espace-Tech-Cloud-CLI
make build
./ghayma version
```

## Documentation

Full documentation at [docs.ghayma.dev/cli](https://docs.ghayma.dev/cli).

## License

MIT
