package cmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
)

var dbCmd = &cobra.Command{
	Use:   "db",
	Short: "Manage databases",
}

var dbCreateType string
var dbCreateReplicaSet bool
var dbCreateTier string
var dbCreateDiskGB int
var dbCreateBackup string

var dbResizeTier string
var dbResizeDiskGB int
var dbResizeBackup string

var dbCreateCmd = &cobra.Command{
	Use:   "create [name]",
	Short: "Create a managed database",
	Args:  requireOneArg("name", ""),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		// Read project_id from the project config
		projectID := ""
		data, err := readProjectConfig(".")
		if err == nil {
			var projectCfg struct {
				ProjectID string `json:"project_id"`
			}
			json.Unmarshal(data, &projectCfg)
			projectID = projectCfg.ProjectID
		}
		if projectID == "" {
			fmt.Println("❌ No project config found. Run 'ghayma init' first or run this command from a project directory.")
			return
		}

		// Only send --replica-set to the server when the user explicitly set
		// it; otherwise let the server apply its default (true for mongodb,
		// ignored for other types).
		var replicaSet *bool
		if cmd.Flags().Changed("replica-set") {
			v := dbCreateReplicaSet
			replicaSet = &v
		}

		client := api.NewClient(cfg)

		tier := dbCreateTier
		diskGB := dbCreateDiskGB
		backup := dbCreateBackup

		// Fail-soft pricing. An older backend (pre-catalog) returns
		// ErrCatalogUnavailable — skip all pricing UI and fall back to bare
		// flags / server defaults; never block the create.
		if cat, catErr := client.GetMarketplaceCatalog(); catErr == nil && cat != nil {
			// Interactive only when the user set NONE of the pricing flags, so
			// a scripted run (any flag present) stays fully non-interactive.
			if !cmd.Flags().Changed("tier") && !cmd.Flags().Changed("disk-gb") && !cmd.Flags().Changed("backup") {
				var selErr error
				tier, diskGB, backup, selErr = promptDBSelections(cat)
				if selErr != nil {
					// A promptui cancel (Ctrl-C) must abort — never fall
					// through to a smallest-tier create on an explicit cancel.
					fmt.Println("❌ Cancelled.")
					return
				}
			}
			// Best-effort reserve preview from the resolved selections,
			// defaulting the unset tier/backup to the catalog's smallest.
			printReservePreview(client, cat, projectID, tier, diskGB, backup)
		}

		db, err := client.CreateDatabase(args[0], dbCreateType, projectID, replicaSet, tier, diskGB, backup)
		if err != nil {
			fmt.Printf("❌ Failed to create database: %s\n", formatMarketplaceError(err))
			return
		}

		fmt.Printf("✅ Created %s database '%s' (linked to project)\n", db.Type, db.Name)
		fmt.Printf("   ID:      %s\n", db.ID)
		fmt.Printf("   Host:    %s\n", db.Host)
		fmt.Printf("   Port:    %d\n", db.Port)
		fmt.Printf("   Status:  %s\n", db.Status)
		if db.Type == "mongodb" {
			fmt.Printf("   Mode:    %s\n", mongoModeLabel(db.ReplicaSet))
		}
	},
}

func mongoModeLabel(replicaSet bool) string {
	if replicaSet {
		return "replica set (rs0) — transactions supported"
	}
	return "standalone — transactions NOT supported"
}

var dbResizeCmd = &cobra.Command{
	Use:   "resize [name]",
	Short: "Change a database's tier, disk (grow-only), or backup schedule",
	Args:  requireOneArg("name", "db list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		db, err := findDatabaseByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		tier := dbResizeTier
		diskGB := dbResizeDiskGB
		backup := dbResizeBackup

		if tier == "" && diskGB == 0 && backup == "" {
			fmt.Println("❌ Nothing to change. Pass at least one of --tier, --disk-gb, --backup.")
			return
		}

		// Disk is grow-only — reject a shrink client-side before the request
		// (local-path PVCs can't shrink safely).
		if diskGB > 0 {
			if msg := diskShrinkError(db.DiskGB, diskGB); msg != "" {
				fmt.Printf("❌ %s\n", msg)
				return
			}
		}

		updated, err := client.RetierDatabase(db.ID, tier, diskGB, backup)
		if err != nil {
			fmt.Printf("❌ Failed to resize database: %s\n", formatMarketplaceError(err))
			return
		}

		fmt.Printf("✅ Database '%s' updated\n", updated.Name)
		fmt.Printf("   Tier:    %s\n", updated.TierSlug)
		fmt.Printf("   Disk:    %d GB\n", updated.DiskGB)
		fmt.Printf("   Backup:  %s\n", updated.BackupTierSlug)
	},
}

// The interactive pickers are indirected through function variables so tests
// can substitute the promptui I/O and exercise the cancel/abort control flow.
var (
	promptDBTierFn   = promptDBTier
	promptDBDiskFn   = promptDBDisk
	promptDBBackupFn = promptDBBackup
)

// promptDBSelections renders the catalog-driven tier/disk/backup pickers for an
// interactive create. Each choice shows its points cost computed from the
// catalog (never hardcoded). A promptui cancel (Ctrl-C) from any picker returns
// a non-nil error so the caller aborts WITHOUT creating a database — an empty
// tiers/backups catalog is NOT a cancel and still degrades to server defaults.
func promptDBSelections(cat *api.MarketplaceCatalog) (string, int, string, error) {
	tier, err := promptDBTierFn(cat)
	if err != nil {
		return "", 0, "", err
	}
	diskGB, err := promptDBDiskFn(cat)
	if err != nil {
		return "", 0, "", err
	}
	backup, err := promptDBBackupFn(cat, diskGB)
	if err != nil {
		return "", 0, "", err
	}
	return tier, diskGB, backup, nil
}

func promptDBTier(cat *api.MarketplaceCatalog) (string, error) {
	tiers := sortedDBTiers(cat)
	if len(tiers) == 0 {
		return "", nil
	}
	labels := make([]string, len(tiers))
	for i, t := range tiers {
		labels[i] = dbTierLabel(t)
	}
	sel := promptui.Select{Label: "Select a database tier", Items: labels, Size: 10}
	idx, _, err := sel.Run()
	if err != nil {
		return "", err
	}
	return tiers[idx].Slug, nil
}

// promptDBDisk asks for a disk size stepped by the catalog's db_block_gb. A
// blank answer leaves it 0 → the server applies its size-based default.
func promptDBDisk(cat *api.MarketplaceCatalog) (int, error) {
	block := int(cat.Rates.DBBlockGB)
	if block <= 0 {
		block = 1
	}
	prompt := promptui.Prompt{
		Label: fmt.Sprintf("Disk in GB — steps of %d (blank for server default)", block),
		Validate: func(s string) error {
			s = strings.TrimSpace(s)
			if s == "" {
				return nil
			}
			n, err := strconv.Atoi(s)
			if err != nil || n <= 0 {
				return fmt.Errorf("enter a positive whole number of GB")
			}
			if n%block != 0 {
				return fmt.Errorf("disk must be a multiple of %d GB", block)
			}
			return nil
		},
	}
	res, err := prompt.Run()
	if err != nil {
		return 0, err
	}
	res = strings.TrimSpace(res)
	if res == "" {
		return 0, nil
	}
	n, _ := strconv.Atoi(res)
	return n, nil
}

func promptDBBackup(cat *api.MarketplaceCatalog, diskGB int) (string, error) {
	tiers := sortedBackupTiers(cat)
	if len(tiers) == 0 {
		return "", nil
	}
	labels := make([]string, len(tiers))
	for i, t := range tiers {
		labels[i] = dbBackupLabel(cat, t, diskGB)
	}
	sel := promptui.Select{Label: "Select a backup schedule", Items: labels, Size: 10}
	idx, _, err := sel.Run()
	if err != nil {
		return "", err
	}
	return tiers[idx].Slug, nil
}

// printReservePreview prints "This database will reserve N pts" before submit,
// with the remaining-after tail when the points summary is fetchable. Every
// step is fail-soft: a missing catalog slug or a failed summary fetch just
// prints less — it never blocks the create.
func printReservePreview(client *api.Client, cat *api.MarketplaceCatalog, projectID, tier string, diskGB int, backup string) {
	previewTier := tier
	if previewTier == "" {
		if dt, ok := defaultDBTier(cat); ok {
			previewTier = dt.Slug
		}
	}
	previewBackup := backup
	if previewBackup == "" {
		if bt, ok := defaultBackupTier(cat); ok {
			previewBackup = bt.Slug
		}
	}
	cost, err := dbCostPreview(cat, previewTier, diskGB, previewBackup)
	if err != nil {
		return
	}
	summary, _ := client.GetProjectPoints(projectID)
	fmt.Println(formatReserveLine(cost, summary))
}

// sortedDBTiers / sortedBackupTiers return catalog rows ordered by Position so
// the pickers list smallest→largest regardless of server ordering.
func sortedDBTiers(cat *api.MarketplaceCatalog) []api.CatalogDBTier {
	out := append([]api.CatalogDBTier(nil), cat.DBTiers...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out
}

func sortedBackupTiers(cat *api.MarketplaceCatalog) []api.CatalogBackupTier {
	out := append([]api.CatalogBackupTier(nil), cat.BackupTiers...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out
}

var dbListCmd = &cobra.Command{
	Use:   "list",
	Short: "List your databases",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		databases, err := client.ListDatabases()
		if err != nil {
			fmt.Printf("❌ Failed to list databases: %v\n", err)
			return
		}

		if len(databases) == 0 {
			fmt.Println("No databases found. Create one with: ghayma db create <name> --type postgres")
			return
		}

		fmt.Printf("🗄️  Your databases (%d):\n\n", len(databases))
		for _, db := range databases {
			linked := "unlinked"
			if db.ProjectID != "" {
				linked = "linked"
			}
			fmt.Printf("   %-15s  %-10s  %-10s  %s\n", db.Name, db.Type, db.Status, linked)
		}
	},
}

var dbInfoCmd = &cobra.Command{
	Use:   "info [name]",
	Short: "Show database details",
	Args:  requireOneArg("name", "db list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		db, err := findDatabaseByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		fmt.Printf("🗄️  Database: %s\n\n", db.Name)
		fmt.Printf("   Type:       %s %s\n", db.Type, db.Version)
		fmt.Printf("   Status:     %s\n", db.Status)
		fmt.Printf("   Host:       %s\n", db.Host)
		fmt.Printf("   Port:       %d\n", db.Port)
		if db.DBName != "" {
			fmt.Printf("   Database:   %s\n", db.DBName)
			fmt.Printf("   Username:   %s\n", db.Username)
		}
		fmt.Printf("   Storage:    %d MB\n", db.StorageMB)
		fmt.Printf("   CPU:        %s\n", db.CPULimit)
		fmt.Printf("   Memory:     %s\n", db.MemoryLimit)
		if db.ProjectID != "" {
			fmt.Printf("   Linked to:  %s\n", db.ProjectID)
		} else {
			fmt.Printf("   Linked to:  (none)\n")
		}
	},
}

var dbLinkProject string

var dbLinkCmd = &cobra.Command{
	Use:   "link [db-name]",
	Short: "Link a database to a project",
	Args:  requireOneArg("db-name", "db list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)

		// Find database by name
		db, err := findDatabaseByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		// Find project by name/slug or from .espacetech.json
		projectID := ""
		if dbLinkProject != "" {
			projects, err := client.ListProjects()
			if err != nil {
				fmt.Printf("❌ Failed to list projects: %v\n", err)
				return
			}
			for _, p := range projects {
				if p.Name == dbLinkProject || p.Slug == dbLinkProject {
					projectID = p.ID
					break
				}
			}
			if projectID == "" {
				fmt.Printf("❌ Project '%s' not found\n", dbLinkProject)
				return
			}
		} else {
			data, err := readProjectConfig(".")
			if err != nil {
				fmt.Println("❌ No --project flag and no project config found")
				return
			}
			var projectCfg struct {
				ProjectID string `json:"project_id"`
			}
			json.Unmarshal(data, &projectCfg)
			projectID = projectCfg.ProjectID
		}

		err = client.LinkDatabase(db.ID, projectID)
		if err != nil {
			fmt.Printf("❌ Failed to link: %v\n", err)
			return
		}

		fmt.Printf("✅ Database '%s' linked to project. Connection string injected as env var.\n", args[0])
	},
}

var dbUnlinkCmd = &cobra.Command{
	Use:   "unlink [db-name]",
	Short: "Unlink a database from its project",
	Args:  requireOneArg("db-name", "db list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		db, err := findDatabaseByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		err = client.UnlinkDatabase(db.ID)
		if err != nil {
			fmt.Printf("❌ Failed to unlink: %v\n", err)
			return
		}

		fmt.Printf("✅ Database '%s' unlinked. Env var removed from project.\n", args[0])
	},
}

var dbDeleteCmd = &cobra.Command{
	Use:   "delete [name]",
	Short: "Delete a database and all its data",
	Args:  requireOneArg("name", "db list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		db, err := findDatabaseByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		fmt.Printf("⚠️  This will permanently delete '%s' and all its data. Type the database name to confirm: ", args[0])
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != args[0] {
			fmt.Println("❌ Cancelled.")
			return
		}

		err = client.DeleteDatabase(db.ID)
		if err != nil {
			fmt.Printf("❌ Failed to delete: %v\n", err)
			return
		}

		fmt.Printf("✅ Database '%s' deleted.\n", args[0])
	},
}

// findDatabaseByName looks up a database by name from the user's list
func findDatabaseByName(client *api.Client, name string) (*api.DatabaseInfo, error) {
	databases, err := client.ListDatabases()
	if err != nil {
		return nil, fmt.Errorf("failed to list databases: %v", err)
	}
	for _, db := range databases {
		if db.Name == name {
			return &db, nil
		}
	}
	return nil, fmt.Errorf("database '%s' not found", name)
}

var dbExposeCmd = &cobra.Command{
	Use:   "expose [name]",
	Short: "Enable external access to a database",
	Args:  requireOneArg("name", "db list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		db, err := findDatabaseByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		result, err := client.ExposeDatabase(db.ID)
		if err != nil {
			fmt.Printf("❌ Failed to expose: %v\n", err)
			return
		}

		fmt.Printf("✅ Database '%s' exposed externally\n", args[0])
		fmt.Printf("   Host: %v\n", result["external_host"])
		fmt.Printf("   Port: %v\n", result["external_port"])
		fmt.Printf("   Connection: %v\n", result["connection"])
		fmt.Println("\n📋 Get full credentials with:")
		fmt.Printf("   ghayma db credentials %s\n", args[0])
	},
}

var dbUnexposeCmd = &cobra.Command{
	Use:   "unexpose [name]",
	Short: "Disable external access to a database",
	Args:  requireOneArg("name", "db list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		db, err := findDatabaseByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		err = client.UnexposeDatabase(db.ID)
		if err != nil {
			fmt.Printf("❌ Failed to unexpose: %v\n", err)
			return
		}

		fmt.Printf("✅ External access disabled for '%s'\n", args[0])
	},
}

var dbCredentialsCmd = &cobra.Command{
	Use:   "credentials [name]",
	Short: "Show database connection credentials",
	Args:  requireOneArg("name", "db list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		db, err := findDatabaseByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		creds, err := client.GetDatabaseCredentials(db.ID)
		if err != nil {
			fmt.Printf("❌ Failed to get credentials: %v\n", err)
			return
		}

		fmt.Printf("🔑 Credentials for '%s' (%s)\n\n", args[0], creds["type"])
		fmt.Printf("   Host:     %v\n", creds["host"])
		fmt.Printf("   Port:     %v\n", creds["port"])
		if creds["username"] != nil && creds["username"] != "" {
			fmt.Printf("   Username: %v\n", creds["username"])
		}
		if creds["database"] != nil && creds["database"] != "" {
			fmt.Printf("   Database: %v\n", creds["database"])
		}
		fmt.Printf("   Password: %v\n", creds["password"])
		fmt.Printf("\n   Internal URL: %v\n", creds["internal_url"])

		if creds["external_access"] == true {
			fmt.Printf("   External URL: %v\n", creds["external_url"])
		} else {
			fmt.Println("\n   ℹ️  External access is off. Enable with:")
			fmt.Printf("      ghayma db expose %s\n", args[0])
		}
	},
}

var dbStopCmd = &cobra.Command{
	Use:   "stop [name]",
	Short: "Stop a database (preserves data)",
	Args:  requireOneArg("name", "db list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		db, err := findDatabaseByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		err = client.StopDatabase(db.ID)
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		fmt.Printf("✅ Database '%s' stopped. Data is preserved.\n", args[0])
		fmt.Printf("   Restart with: ghayma db start %s\n", args[0])
	},
}

var dbStartCmd = &cobra.Command{
	Use:   "start [name]",
	Short: "Start a stopped database",
	Args:  requireOneArg("name", "db list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		db, err := findDatabaseByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		err = client.StartDatabase(db.ID)
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		fmt.Printf("✅ Database '%s' started.\n", args[0])
	},
}

var dbRotateCmd = &cobra.Command{
	Use:   "rotate [name]",
	Short: "Rotate database password",
	Args:  requireOneArg("name", "db list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		db, err := findDatabaseByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		fmt.Printf("⚠️  This will change the password for '%s'. Connected clients will need to reconnect. Continue? (y/n): ", args[0])
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != "y" && confirm != "Y" {
			fmt.Println("❌ Cancelled.")
			return
		}

		result, err := client.RotatePassword(db.ID)
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		fmt.Printf("✅ Password rotated for '%s'\n", args[0])
		fmt.Printf("   New password: %v\n", result["new_password"])
		fmt.Println("\n   ⚠️  Save this password now — it won't be shown again.")
		fmt.Printf("   Get full connection string: ghayma db credentials %s\n", args[0])
	},
}

func init() {
	dbCreateCmd.Flags().StringVarP(&dbCreateType, "type", "t", "postgres", "Database type: postgres, redis, mongodb")
	dbCreateCmd.Flags().BoolVar(&dbCreateReplicaSet, "replica-set", true, "MongoDB only: run as a single-node replica set (rs0). Default true so multi-document transactions work. Pass --replica-set=false for a standalone mongod.")
	dbCreateCmd.Flags().StringVar(&dbCreateTier, "tier", "", "Database tier (e.g. xs, s, m, l). Interactive picker when omitted; server default if no catalog.")
	dbCreateCmd.Flags().IntVar(&dbCreateDiskGB, "disk-gb", 0, "Persistent disk in GB, priced in points. Server default (from size) when omitted.")
	dbCreateCmd.Flags().StringVar(&dbCreateBackup, "backup", "", "Backup schedule: weekly, daily, sixhourly. Interactive picker when omitted; weekly default if no catalog.")
	// --project has no short form; -p is reserved for --prod on `deploy`.
	dbLinkCmd.Flags().StringVar(&dbLinkProject, "project", "", "Project name or slug (defaults to current directory's project)")

	dbResizeCmd.Flags().StringVar(&dbResizeTier, "tier", "", "New database tier (e.g. xs, s, m, l)")
	dbResizeCmd.Flags().IntVar(&dbResizeDiskGB, "disk-gb", 0, "New disk size in GB (grow-only — cannot shrink)")
	dbResizeCmd.Flags().StringVar(&dbResizeBackup, "backup", "", "New backup schedule: weekly, daily, sixhourly")

	dbCmd.AddCommand(dbCreateCmd)
	dbCmd.AddCommand(dbListCmd)
	dbCmd.AddCommand(dbInfoCmd)
	dbCmd.AddCommand(dbLinkCmd)
	dbCmd.AddCommand(dbUnlinkCmd)
	dbCmd.AddCommand(dbDeleteCmd)
	rootCmd.AddCommand(dbCmd)
	dbCmd.AddCommand(dbExposeCmd)
	dbCmd.AddCommand(dbUnexposeCmd)
	dbCmd.AddCommand(dbCredentialsCmd)
	dbCmd.AddCommand(dbStopCmd)
	dbCmd.AddCommand(dbStartCmd)
	dbCmd.AddCommand(dbRotateCmd)
	dbCmd.AddCommand(dbResizeCmd)
}
