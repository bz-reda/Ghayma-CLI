package cmd

import (
	"encoding/json"
	"fmt"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

var storageCmd = &cobra.Command{
	Use:   "storage",
	Short: "Manage object storage buckets",
}

var storageCreateQuotaGB int

var storageCreateCmd = &cobra.Command{
	Use:   "create [name]",
	Short: "Create a storage bucket",
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

		client := api.NewClient(cfg)

		quotaGB := storageCreateQuotaGB

		// Fail-soft pricing. An older backend (pre-catalog) returns
		// ErrCatalogUnavailable — skip all pricing UI and fall back to the bare
		// flag / server default; never block the create.
		if cat, catErr := client.GetMarketplaceCatalog(); catErr == nil && cat != nil {
			// Interactive only when the user did NOT set --quota-gb, so a
			// scripted run (flag present) stays fully non-interactive.
			if !cmd.Flags().Changed("quota-gb") {
				q, selErr := promptStorageQuotaFn(cat)
				if selErr != nil {
					// A promptui cancel (Ctrl-C) must abort — never fall
					// through to a create at a default quota.
					fmt.Println("❌ Cancelled.")
					return
				}
				quotaGB = q
			}
			// Reserve preview only when a quota is chosen: an unset quota uses
			// the plan's per-bucket default, whose MB the CLI can't know, so a
			// "reserve 0 pts" line would be misleading.
			if quotaGB > 0 {
				cost := storageCostPreview(cat, quotaGB)
				summary, _ := client.GetProjectPoints(projectID)
				fmt.Println(formatReserveLineFor("bucket", cost, summary))
			}
		}

		// --quota-gb is whole GB; the backend field is size_mb (no quota_gb).
		sizeMB := quotaGB * 1024
		bucket, err := client.CreateBucket(args[0], projectID, sizeMB)
		if err != nil {
			fmt.Printf("❌ Failed to create bucket: %s\n", formatMarketplaceError(err))
			return
		}

		fmt.Printf("✅ Created storage bucket '%s' (linked to project)\n", bucket.Name)
		fmt.Printf("   ID:       %s\n", bucket.ID)
		fmt.Printf("   Bucket:   %s\n", bucket.GarageBucket)
		fmt.Printf("   Limit:    %s\n", formatBytes(bucket.StorageLimitBytes))
		fmt.Printf("   Status:   %s\n", bucket.Status)
	},
}

var storageListCmd = &cobra.Command{
	Use:   "list",
	Short: "List your storage buckets",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		buckets, err := client.ListBuckets()
		if err != nil {
			fmt.Printf("❌ Failed to list buckets: %v\n", err)
			return
		}

		if len(buckets) == 0 {
			fmt.Println("No storage buckets found. Create one with: ghayma storage create <name>")
			return
		}

		fmt.Printf("📦 Your storage buckets (%d):\n\n", len(buckets))
		for _, b := range buckets {
			linked := "unlinked"
			if b.ProjectID != "" {
				linked = "linked"
			}
			public := ""
			if b.ExternalAccess {
				public = " 🌐"
			}
			fmt.Printf("   %-15s  %-8s  %s/%s  %s%s\n",
				b.Name, b.Status,
				formatBytes(b.StorageUsedBytes), formatBytes(b.StorageLimitBytes),
				linked, public)
		}
	},
}

var storageInfoCmd = &cobra.Command{
	Use:   "info [name]",
	Short: "Show bucket details",
	Args:  requireOneArg("name", "storage list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		bucket, err := findBucketByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		fmt.Printf("📦 Bucket: %s\n\n", bucket.Name)
		fmt.Printf("   S3 Bucket:  %s\n", bucket.GarageBucket)
		fmt.Printf("   Status:     %s\n", bucket.Status)
		fmt.Printf("   Storage:    %s / %s\n", formatBytes(bucket.StorageUsedBytes), formatBytes(bucket.StorageLimitBytes))
		fmt.Printf("   Public:     %v\n", bucket.ExternalAccess)
		if bucket.ProjectID != "" {
			fmt.Printf("   Linked to:  %s\n", bucket.ProjectID)
		} else {
			fmt.Printf("   Linked to:  (none)\n")
		}
		fmt.Printf("   Endpoint:   https://s3.ghayma.tech\n")
		if bucket.ExternalAccess {
			fmt.Printf("   Public URL: https://%s.web.ghayma.tech\n", bucket.GarageBucket)
		}
	},
}

var storageCredentialsCmd = &cobra.Command{
	Use:   "credentials [name]",
	Short: "Show S3 access credentials",
	Args:  requireOneArg("name", "storage list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		bucket, err := findBucketByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		creds, err := client.GetBucketCredentials(bucket.ID)
		if err != nil {
			fmt.Printf("❌ Failed to get credentials: %v\n", err)
			return
		}

		fmt.Printf("🔑 S3 Credentials for '%s'\n\n", args[0])
		fmt.Printf("   Endpoint:    %v\n", creds["endpoint"])
		fmt.Printf("   Region:      %v\n", creds["region"])
		fmt.Printf("   Bucket:      %v\n", creds["bucket"])
		fmt.Printf("   Access Key:  %v\n", creds["access_key"])
		fmt.Printf("   Secret Key:  %v\n", creds["secret_key"])
		fmt.Println("\n   📋 Use with any S3-compatible SDK (aws-sdk, boto3, etc.)")
	},
}

var storageLinkProject string

var storageLinkCmd = &cobra.Command{
	Use:   "link [bucket-name]",
	Short: "Link a bucket to a project (injects S3 env vars)",
	Args:  requireOneArg("bucket-name", "storage list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		bucket, err := findBucketByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		projectID := ""
		if storageLinkProject != "" {
			projects, err := client.ListProjects()
			if err != nil {
				fmt.Printf("❌ Failed to list projects: %v\n", err)
				return
			}
			for _, p := range projects {
				if p.Name == storageLinkProject || p.Slug == storageLinkProject {
					projectID = p.ID
					break
				}
			}
			if projectID == "" {
				fmt.Printf("❌ Project '%s' not found\n", storageLinkProject)
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

		err = client.LinkBucket(bucket.ID, projectID)
		if err != nil {
			fmt.Printf("❌ Failed to link: %v\n", err)
			return
		}

		fmt.Printf("✅ Bucket '%s' linked to project\n", args[0])
		fmt.Println("   Env vars injected: STORAGE_ENDPOINT, STORAGE_ACCESS_KEY, STORAGE_SECRET_KEY, STORAGE_BUCKET, STORAGE_REGION")
	},
}

var storageUnlinkCmd = &cobra.Command{
	Use:   "unlink [bucket-name]",
	Short: "Unlink a bucket from its project",
	Args:  requireOneArg("bucket-name", "storage list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		bucket, err := findBucketByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		err = client.UnlinkBucket(bucket.ID)
		if err != nil {
			fmt.Printf("❌ Failed to unlink: %v\n", err)
			return
		}

		fmt.Printf("✅ Bucket '%s' unlinked. S3 env vars removed from project.\n", args[0])
	},
}

var storageExposeCmd = &cobra.Command{
	Use:   "expose [name]",
	Short: "Make bucket publicly accessible as a website",
	Args:  requireOneArg("name", "storage list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		bucket, err := findBucketByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		result, err := client.ExposeBucket(bucket.ID)
		if err != nil {
			fmt.Printf("❌ Failed to expose: %v\n", err)
			return
		}

		fmt.Printf("✅ Bucket '%s' is now publicly accessible\n", args[0])
		fmt.Printf("   🌐 Public URL: %v\n", result["public_url"])
	},
}

var storageUnexposeCmd = &cobra.Command{
	Use:   "unexpose [name]",
	Short: "Disable public access to a bucket",
	Args:  requireOneArg("name", "storage list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		bucket, err := findBucketByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		err = client.UnexposeBucket(bucket.ID)
		if err != nil {
			fmt.Printf("❌ Failed to unexpose: %v\n", err)
			return
		}

		fmt.Printf("✅ Public access disabled for '%s'\n", args[0])
	},
}

var storageRotateCmd = &cobra.Command{
	Use:   "rotate [name]",
	Short: "Rotate S3 access credentials",
	Args:  requireOneArg("name", "storage list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		bucket, err := findBucketByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		fmt.Printf("⚠️  This will invalidate current credentials for '%s'. Continue? (y/n): ", args[0])
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != "y" && confirm != "Y" {
			fmt.Println("❌ Cancelled.")
			return
		}

		creds, err := client.RotateBucketCredentials(bucket.ID)
		if err != nil {
			fmt.Printf("❌ Failed to rotate: %v\n", err)
			return
		}

		fmt.Printf("✅ Credentials rotated for '%s'\n\n", args[0])
		fmt.Printf("   Access Key:  %v\n", creds["access_key"])
		fmt.Printf("   Secret Key:  %v\n", creds["secret_key"])
		fmt.Println("\n   ⚠️  Save these now — the secret key won't be shown again.")
	},
}

var storageDeleteCmd = &cobra.Command{
	Use:   "delete [name]",
	Short: "Delete a storage bucket and all its data",
	Args:  requireOneArg("name", "storage list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		bucket, err := findBucketByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		fmt.Printf("⚠️  This will permanently delete '%s' and all its files. Type the bucket name to confirm: ", args[0])
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != args[0] {
			fmt.Println("❌ Cancelled.")
			return
		}

		err = client.DeleteBucket(bucket.ID)
		if err != nil {
			fmt.Printf("❌ Failed to delete: %v\n", err)
			return
		}

		fmt.Printf("✅ Bucket '%s' deleted.\n", args[0])
	},
}

func findBucketByName(client *api.Client, name string) (*api.BucketInfo, error) {
	buckets, err := client.ListBuckets()
	if err != nil {
		return nil, fmt.Errorf("failed to list buckets: %v", err)
	}
	for _, b := range buckets {
		if b.Name == name {
			return &b, nil
		}
	}
	return nil, fmt.Errorf("bucket '%s' not found", name)
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func init() {
	storageCreateCmd.Flags().IntVar(&storageCreateQuotaGB, "quota-gb", 0, "Per-bucket storage quota in GB, priced in points (stepped by the catalog's obj_block_gb). Interactive picker when omitted; plan default if no catalog.")
	// --project has no short form; -p is reserved for --prod on `deploy`.
	storageLinkCmd.Flags().StringVar(&storageLinkProject, "project", "", "Project name or slug")

	storageCmd.AddCommand(storageCreateCmd)
	storageCmd.AddCommand(storageListCmd)
	storageCmd.AddCommand(storageInfoCmd)
	storageCmd.AddCommand(storageCredentialsCmd)
	storageCmd.AddCommand(storageLinkCmd)
	storageCmd.AddCommand(storageUnlinkCmd)
	storageCmd.AddCommand(storageExposeCmd)
	storageCmd.AddCommand(storageUnexposeCmd)
	storageCmd.AddCommand(storageRotateCmd)
	storageCmd.AddCommand(storageDeleteCmd)
	rootCmd.AddCommand(storageCmd)
}