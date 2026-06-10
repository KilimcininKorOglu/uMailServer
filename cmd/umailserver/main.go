package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	// Embed the IANA timezone database in the binary so time.LoadLocation works
	// regardless of the (minimal alpine) runtime image, which ships no tzdata.
	// Required by per-user timezone rendering (outbound Date header, profile
	// timezone validation) — without it LoadLocation fails for every non-UTC zone.
	_ "time/tzdata"

	"github.com/umailserver/umailserver/internal/auth"
	"github.com/umailserver/umailserver/internal/caldav"
	"github.com/umailserver/umailserver/internal/carddav"
	"github.com/umailserver/umailserver/internal/cli"
	"github.com/umailserver/umailserver/internal/config"
	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/db/migrations"
	"github.com/umailserver/umailserver/internal/db/postgres"
	"github.com/umailserver/umailserver/internal/ews"
	"github.com/umailserver/umailserver/internal/imap"
	"github.com/umailserver/umailserver/internal/mailcheck"
	"github.com/umailserver/umailserver/internal/mailexport"
	"github.com/umailserver/umailserver/internal/mailimport"
	"github.com/umailserver/umailserver/internal/migratestore"
	"github.com/umailserver/umailserver/internal/pimport"
	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/server"
	"github.com/umailserver/umailserver/internal/storage"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
)

var (
	Version   = "dev"
	BuildDate = "unknown"
	GitCommit = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		cmdServe(os.Args[2:])
	case "quickstart":
		cmdQuickstart(os.Args[2:])
	case "domain":
		cmdDomain(os.Args[2:])
	case "account":
		cmdAccount(os.Args[2:])
	case "queue":
		cmdQueue(os.Args[2:])
	case "check":
		cmdCheck(os.Args[2:])
	case "test":
		cmdTest(os.Args[2:])
	case "backup":
		cmdBackup(os.Args[2:])
	case "restore":
		cmdRestore(os.Args[2:])
	case "migrate":
		cmdMigrate(os.Args[2:])
	case "import":
		cmdImport(os.Args[2:])
	case "export":
		cmdExport(os.Args[2:])
	case "mbsize":
		cmdMbsize(os.Args[2:])
	case "db":
		cmdDB(os.Args[2:])
	case "status":
		cmdStatus(os.Args[2:])
	case "stop":
		cmdStop(os.Args[2:])
	case "restart":
		cmdRestart(os.Args[2:])
	case "version":
		fmt.Printf("uMailServer %s (%s) built %s\n", Version, GitCommit, BuildDate)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`uMailServer - One binary. Complete email.

Usage: umailserver <command> [flags]

Commands:
  serve        Start the mail server
  stop         Stop the running server
  restart      Restart the server
  status       Show server status
  quickstart   Generate config and create first account
  domain       Domain management (add, list, dns)
  account      Account management (add, password, list, delete)
  queue        Queue management (list, retry, flush, drop)
  check        Diagnostics (dns, tls, deliverability, mailbox)
  test         Test utilities (send)
  backup       Create backup
  restore      Restore from backup
  migrate      Import from other mail servers
  import       Import mbox/.eml/Maildir/.ics/.vcf into a mailbox (server stopped)
  export       Export a mailbox to mbox/.eml/Maildir/.ics/.vcf (server stopped)
  mbsize       Report mailbox storage size (per-folder + quota)
  db           Database management (migrate, status)
  version      Show version

Examples:
  umailserver quickstart you@example.com
  umailserver serve --config /etc/umailserver.yaml
  umailserver status
  umailserver stop
  umailserver domain add example.com
  umailserver account add john@example.com
  umailserver check dns example.com`)
}

func cmdServe(args []string) {
	var configPath string
	var dataDir string

	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	fs.StringVar(&configPath, "config", "", "Path to config file")
	fs.StringVar(&dataDir, "data-dir", "", "Override data directory")
	_ = fs.Parse(args)

	// Check if this is first run (no config exists)
	if configPath == "" && config.CheckFirstRun(dataDir) {
		fmt.Println()
		fmt.Println("Welcome to uMailServer!")
		fmt.Println("It looks like this is your first time running the server.")
		fmt.Println()

		// Run interactive setup
		wizard := config.NewSetupWizard()
		wizard.Config.Server.DataDir = dataDir

		cfg, err := wizard.Run()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Setup failed: %v\n", err)
			os.Exit(1)
		}

		// Use the newly created config
		configPath = filepath.Join(dataDir, "config.yaml")

		fmt.Println()
		fmt.Println("Setup complete! Starting server...")
		fmt.Println()

		// Update cfg variable for use below
		_ = cfg.EnsureDataDir()
	}

	// Load configuration
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Override data directory if explicitly specified via flag
	if dataDir != "" {
		cfg.Server.DataDir = dataDir
	}

	// Create server
	srv, err := server.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create server: %v\n", err)
		os.Exit(1)
	}

	// Record the loaded config path so the admin config API can persist
	// runtime changes back to it.
	srv.SetConfigPath(configPath)

	// Start server
	if err := srv.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start server: %v\n", err)
		os.Exit(1)
	}

	// Wait for shutdown signal
	if err := srv.Wait(); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

func cmdQuickstart(args []string) {
	// Define defaults to match install.sh
	defaultDataDir := "/var/lib/umailserver"
	defaultConfigDir := "/etc/umailserver"
	defaultConfigPath := defaultConfigDir + "/umailserver.yaml"

	// Command-line flags
	var dataDir string
	var configPath string

	fs := flag.NewFlagSet("quickstart", flag.ExitOnError)
	fs.StringVar(&dataDir, "data-dir", defaultDataDir, "Data directory")
	fs.StringVar(&configPath, "config", defaultConfigPath, "Config file path")
	_ = fs.Parse(args)

	// Get email from remaining args
	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: umailserver quickstart <email> [flags]")
		fmt.Println("Flags:")
		fs.PrintDefaults()
		os.Exit(1)
	}

	email := remaining[0]
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		fmt.Fprintf(os.Stderr, "Invalid email format: %s\n", email)
		os.Exit(1)
	}
	domain := parts[1]

	// Check if config already exists
	if _, err := os.Stat(configPath); err == nil {
		fmt.Printf("Config file already exists: %s\n", configPath)
		fmt.Print("Overwrite? (y/N): ")
		var response string
		_, _ = fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Aborted.")
			os.Exit(0)
		}
	}

	// Generate config
	fmt.Println("=== uMailServer Quickstart ===")
	fmt.Printf("Setting up for: %s\n\n", email)

	// Generate DKIM key
	dkimDir := filepath.Join(dataDir, "dkim")
	_ = os.MkdirAll(dkimDir, 0o750)
	dkimKeyPath := filepath.Join(dkimDir, domain+".private.pem")

	fmt.Println("Generating DKIM key pair...")
	if err := generateDKIMKey(dkimKeyPath); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to generate DKIM key: %v\n", err)
		os.Exit(1)
	}

	// Read public key for DNS
	publicKey, err := os.ReadFile(filepath.Clean(dkimKeyPath + ".pub"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read DKIM public key: %v\n", err)
		os.Exit(1)
	}

	// Write config
	config := fmt.Sprintf(`# uMailServer Configuration
# Generated by quickstart for %s

server:
  hostname: mail.%s
  data_dir: %s

tls:
  acme:
    enabled: true
    email: %s
    provider: letsencrypt

smtp:
  inbound:
    port: 25
    max_message_size: 52428800  # 50MB
    max_recipients: 100
  submission:
    port: 587
    require_auth: true
    require_tls: true
  submission_tls:
    port: 465

imap:
  port: 993

http:
  port: 443
  http_port: 80

admin:
  enabled: true
  port: 8443
  bind: 127.0.0.1

spam:
  reject_threshold: 9.0
  junk_threshold: 3.0
  greylisting:
    enabled: true
    delay: 5m

domains:
  - name: %s
    max_accounts: 100
    max_mailbox_size: 5368709120  # 5GB
    dkim:
      selector: default
`, email, domain, dataDir, email, domain)

	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Config written to: %s\n", configPath)
	fmt.Printf("✓ Data directory: %s\n", dataDir)

	// Initialize database
	fmt.Println("\nInitializing database...")
	dbPath := filepath.Join(dataDir, "umailserver.db")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create data directory: %v\n", err)
		os.Exit(1)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	// Run pending migrations
	fmt.Println("Running database migrations...")
	registry := migrations.NewRegistry()
	migrations.InitMigrations(registry)
	migrator := migrations.NewMigrator(database.BoltDB(), registry)
	if err := migrator.Migrate(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to run migrations: %v\n", err)
		os.Exit(1)
	}

	// Create domain
	if err := database.CreateDomain(&db.DomainData{
		Name:        domain,
		MaxAccounts: 100,
		IsActive:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create domain: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Domain created: %s\n", domain)

	// Create admin account with password prompt
	fmt.Print("\nEnter admin password: ")
	password := readPassword()

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to hash password: %v\n", err)
		os.Exit(1)
	}

	if err := database.CreateAccount(&db.AccountData{
		Email:        email,
		LocalPart:    parts[0],
		Domain:       domain,
		PasswordHash: string(hash),
		IsAdmin:      true,
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create account: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Admin account created: %s\n", email)

	// Print DNS records
	fmt.Println("\n=== Required DNS Records ===")
	fmt.Println("\n# MX Record:")
	fmt.Printf("%s.    IN    MX    10    mail.%s.\n\n", domain, domain)

	fmt.Println("# A Records:")
	fmt.Printf("mail.%s.    IN    A    <YOUR_SERVER_IP>\n\n", domain)

	fmt.Println("# SPF Record:")
	fmt.Printf("%s.    IN    TXT    \"v=spf1 mx ~all\"\n\n", domain)

	fmt.Println("# DKIM Record (default._domainkey):")
	dkimRecord := fmt.Sprintf("v=DKIM1; k=rsa; p=%s", strings.TrimSpace(string(publicKey)))
	fmt.Printf("default._domainkey.%s.    IN    TXT    \"%s\"\n\n", domain, dkimRecord)

	fmt.Println("# DMARC Record:")
	fmt.Printf("_dmarc.%s.    IN    TXT    \"v=DMARC1; p=quarantine; rua=mailto:dmarc@%s\"\n\n", domain, domain)

	fmt.Println("=== Next Steps ===")
	fmt.Println("1. Update DNS records above with your actual server IP")
	fmt.Println("2. Start the server: sudo systemctl start umailserver")
	fmt.Println("   Or run directly: umailserver serve")
	fmt.Println("3. Access webmail at: https://mail.yourdomain.com")
	fmt.Println("4. Access admin panel at: https://127.0.0.1:8443")
}

func generateDKIMKey(keyPath string) error {
	// Generate RSA key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}

	// Write private key
	privateKeyPEM := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}

	privateKeyFile, err := os.Create(filepath.Clean(keyPath))
	if err != nil {
		return err
	}
	defer privateKeyFile.Close()

	if err := pem.Encode(privateKeyFile, privateKeyPEM); err != nil {
		return err
	}

	// Write public key
	publicKeyBytes, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return err
	}

	publicKeyPEM := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: publicKeyBytes,
	}

	publicKeyFile, err := os.Create(filepath.Clean(keyPath + ".pub"))
	if err != nil {
		return err
	}
	defer publicKeyFile.Close()

	if err := pem.Encode(publicKeyFile, publicKeyPEM); err != nil {
		return err
	}

	return nil
}

func readPassword() string {
	// #nosec G115 -- file descriptors are small positive integers on all supported platforms
	fd := int(os.Stdin.Fd())
	if state, err := term.MakeRaw(fd); err == nil {
		defer func() { _ = term.Restore(fd, state) }()
		if pw, err := term.ReadPassword(fd); err == nil {
			fmt.Println()
			return string(pw)
		}
	}
	// Fallback for non-terminal contexts
	var password string
	_, _ = fmt.Scanln(&password)
	return password
}

func cliConfigPaths() []string {
	paths := []string{}
	if envPath := os.Getenv("UMAILSERVER_CONFIG"); envPath != "" {
		paths = append(paths, envPath)
	}
	paths = append(paths,
		"./umailserver.yaml",
		"./umailserver.yml",
		"./demo.yaml",
		"/etc/umailserver/umailserver.yaml",
		"/etc/umailserver.yaml",
	)
	return paths
}

func loadCLIConfig() (*config.Config, error) {
	for _, path := range cliConfigPaths() {
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}

		cfg, err := config.Load(path)
		if err != nil {
			return nil, err
		}
		return cfg, nil
	}

	return config.Load("")
}

func getDataDir() string {
	cfg, err := loadCLIConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}
	return cfg.Server.DataDir
}

func getDatabasePath() string {
	cfg, err := loadCLIConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}
	return cfg.DatabasePath()
}

// openConfiguredStore opens the account/metadata store on the backend the
// config selects (bbolt by default, PostgreSQL when database.backend is
// "postgres"), via the server's single canonical opener. The account, domain,
// and queue subcommands use this so they operate on the same store the running
// server does instead of hard-opening the embedded bbolt file. Exits on error.
func openConfiguredStore() db.Store {
	cfg, err := loadCLIConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}
	store, err := server.OpenStore(context.Background(), cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	return store
}

// mailCLIStores bundles the canonical stores the offline mail CLI
// (import/export/check/repair) operates on, opened for the configured backend.
// Message BODIES are always Maildir files (blob); the metadata index and semcore
// identity are bbolt (three separate single-writer files) or PostgreSQL (one
// shared *postgres.DB serving account, index, and identity). The CLI talks to
// the index and identity through the backend-agnostic imap.MetadataStore and
// ews.IdentityStore interfaces, which both engines satisfy.
type mailCLIStores struct {
	account  db.Store
	index    imap.MetadataStore
	blob     *storage.MessageStore
	identity ews.IdentityStore
	pipe     *semcore.MutationPipeline
	cal      *caldav.CollabStore     // canonical calendar store (PIM import/export)
	task     *caldav.CollabTaskStore // canonical tasks store (VTODO import/export)
	card     *carddav.CollabStore    // canonical contacts store (PIM import/export)
	close    func()
}

// Compile-time proof that both backends satisfy the interfaces the mail CLI
// drives them through (the server enforces the same elsewhere; restated here so
// the CLI's dependency is explicit).
var (
	_ imap.MetadataStore = (*storage.Database)(nil)
	_ imap.MetadataStore = (*postgres.DB)(nil)
	_ ews.IdentityStore  = (*semcore.BoltIdentityStore)(nil)
	_ ews.IdentityStore  = (*postgres.DB)(nil)
)

// openMailCLIStores opens the account store (via the server's canonical opener,
// so the backend matches the running server), the Maildir blob store, the
// message-metadata index, the semcore identity store, and a mutation pipeline,
// for whichever backend the config selects. bbolt is single-writer, so on that
// backend these fail if the server is running; run with the server STOPPED.
// Callers must invoke the returned close func.
func openMailCLIStores(cfg *config.Config) (*mailCLIStores, error) {
	dataDir := cfg.Server.DataDir
	blob, err := storage.NewMessageStoreWithOptions(filepath.Join(dataDir, "mail", "messages"), cfg.Storage.Sync)
	if err != nil {
		return nil, fmt.Errorf("open message store: %w", err)
	}
	account, err := server.OpenStore(context.Background(), cfg)
	if err != nil {
		return nil, err
	}

	if cfg.DatabaseBackend() == "postgres" {
		pg, ok := account.(*postgres.DB)
		if !ok {
			closeImportStore("database", account)
			return nil, fmt.Errorf("postgres backend: account store is %T, not *postgres.DB", account)
		}
		// One *postgres.DB serves account, index, identity, and collaboration.
		return &mailCLIStores{
			account:  pg,
			index:    pg,
			blob:     blob,
			identity: pg,
			pipe:     semcore.NewMutationPipeline(pg, pg),
			cal:      caldav.NewCollabStore(pg, pg),
			task:     caldav.NewCollabTaskStore(pg, pg),
			card:     carddav.NewCollabStore(pg, pg),
			close:    func() { closeImportStore("database", pg) },
		}, nil
	}

	storageDB, err := storage.OpenDatabaseWithOptions(filepath.Join(dataDir, "mail", "mail.db"), cfg.Storage.Sync)
	if err != nil {
		closeImportStore("database", account)
		return nil, fmt.Errorf("open message index (is the server running?): %w", err)
	}
	semStore, err := semcore.NewStore(dataDir)
	if err != nil {
		closeImportStore("message index", storageDB)
		closeImportStore("database", account)
		return nil, fmt.Errorf("open semcore store (is the server running?): %w", err)
	}
	return &mailCLIStores{
		account:  account,
		index:    storageDB,
		blob:     blob,
		identity: semStore.Identity(),
		pipe:     semcore.NewMutationPipeline(semStore.Identity(), semStore.Lifecycle()),
		cal:      caldav.NewCollabStore(semStore.Collaboration(), semStore.Identity()),
		task:     caldav.NewCollabTaskStore(semStore.Collaboration(), semStore.Identity()),
		card:     carddav.NewCollabStore(semStore.Collaboration(), semStore.Identity()),
		close: func() {
			closeImportStore("semcore", semStore)
			closeImportStore("message index", storageDB)
			closeImportStore("database", account)
		},
	}, nil
}

// requireBboltBackend guards the bbolt-only KV-migration subcommands (db
// status/migrate/rollback). Those drive the embedded bbolt migration framework
// via BoltDB(); with database.backend "postgres" the relational schema is
// applied automatically by the server on startup (idempotent), so there is
// nothing for them to do — say so and exit cleanly rather than silently
// creating an empty bbolt file at the configured path.
func requireBboltBackend(action string) {
	cfg, err := loadCLIConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}
	if cfg.DatabaseBackend() == "postgres" {
		fmt.Printf("database.backend is \"postgres\": %q applies only to the embedded bbolt store.\n", action)
		fmt.Println("The PostgreSQL relational schema is created and kept current automatically by the server on startup.")
		os.Exit(0)
	}
}

func cmdDomain(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: umailserver domain <subcommand>")
		fmt.Println("Subcommands: add, list, dns, delete")
		os.Exit(1)
	}

	subcmd := args[0]

	// Open the configured backend (bbolt or postgres).
	database := openConfiguredStore()
	defer database.Close()

	switch subcmd {
	case "add":
		if len(args) < 2 {
			fmt.Println("Usage: umailserver domain add <domain>")
			os.Exit(1)
		}
		domainName := args[1]

		// Generate DKIM key pair
		privKey, _, err := auth.GenerateDKIMKeyPair(2048)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to generate DKIM key: %v\n", err)
			os.Exit(1)
		}
		dkimPublicKey := auth.GetPublicKeyForDNS(privKey)
		dkimPrivateKeyPEM := string(pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(privKey),
		}))

		if err := database.CreateDomain(&db.DomainData{
			Name:           domainName,
			MaxAccounts:    100,
			IsActive:       true,
			DKIMSelector:   "default",
			DKIMPublicKey:  dkimPublicKey,
			DKIMPrivateKey: dkimPrivateKeyPEM,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create domain: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Domain created: %s\n", domainName)
		fmt.Printf("✓ DKIM key generated (selector: default)\n")

	case "list":
		domains, err := database.ListDomains()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to list domains: %v\n", err)
			os.Exit(1)
		}
		if len(domains) == 0 {
			fmt.Println("No domains found.")
			return
		}
		fmt.Println("Domains:")
		fmt.Println("--------")
		for _, d := range domains {
			status := "active"
			if !d.IsActive {
				status = "inactive"
			}
			fmt.Printf("%-30s %s (%d/%d accounts)\n", d.Name, status, 0, d.MaxAccounts)
		}

	case "dns":
		if len(args) < 2 {
			fmt.Println("Usage: umailserver domain dns <domain>")
			os.Exit(1)
		}
		domainName := args[1]
		domain, err := database.GetDomain(domainName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Domain not found: %s\n", domainName)
			os.Exit(1)
		}

		fmt.Printf("\n=== DNS Records for %s ===\n\n", domain.Name)

		fmt.Println("# MX Record:")
		fmt.Printf("%s.    IN    MX    10    mail.%s.\n\n", domain.Name, domain.Name)

		fmt.Println("# A Record:")
		fmt.Printf("mail.%s.    IN    A    <YOUR_SERVER_IP>\n\n", domain.Name)

		fmt.Println("# SPF Record:")
		fmt.Printf("%s.    IN    TXT    \"v=spf1 mx ~all\"\n\n", domain.Name)

		fmt.Println("# DKIM Record (default._domainkey):")
		dkimKey := domain.DKIMPublicKey
		if dkimKey == "" {
			dkimKey = "<GENERATE_WITH: umailserver domain add>"
		}
		fmt.Printf("default._domainkey.%s.    IN    TXT    \"v=DKIM1; k=rsa; p=%s\"\n\n", domain.Name, dkimKey)

		fmt.Println("# DMARC Record:")
		fmt.Printf("_dmarc.%s.    IN    TXT    \"v=DMARC1; p=quarantine; rua=mailto:dmarc@%s\"\n\n", domain.Name, domain.Name)

		fmt.Println("Replace <YOUR_SERVER_IP> with your actual server IP.")

	case "delete":
		if len(args) < 2 {
			fmt.Println("Usage: umailserver domain delete <domain>")
			os.Exit(1)
		}
		domainName := args[1]

		// Confirm deletion
		fmt.Printf("Are you sure you want to delete domain %s? This will delete all accounts! (y/N): ", domainName)
		var response string
		_, _ = fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Aborted.")
			os.Exit(0)
		}

		if err := database.DeleteDomain(domainName); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to delete domain: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Domain deleted: %s\n", domainName)

	default:
		fmt.Fprintf(os.Stderr, "Unknown domain subcommand: %s\n", subcmd)
		os.Exit(1)
	}
}

func cmdAccount(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: umailserver account <subcommand>")
		fmt.Println("Subcommands: add, password, list, delete")
		os.Exit(1)
	}

	subcmd := args[0]

	// Open the configured backend (bbolt or postgres).
	database := openConfiguredStore()
	defer database.Close()

	switch subcmd {
	case "add":
		if len(args) < 2 {
			fmt.Println("Usage: umailserver account add <email>")
			os.Exit(1)
		}
		email := args[1]
		parts := strings.Split(email, "@")
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "Invalid email format: %s\n", email)
			os.Exit(1)
		}

		// Check if domain exists
		_, err := database.GetDomain(parts[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Domain not found: %s (create it first with 'umailserver domain add')\n", parts[1])
			os.Exit(1)
		}

		// Get password (from flag or prompt)
		var password string
		for i, arg := range args {
			if arg == "--password" && i+1 < len(args) {
				password = args[i+1]
				break
			}
		}
		if password == "" {
			fmt.Print("Enter password: ")
			password = readPassword()
		}
		if len(password) < 8 {
			fmt.Fprintf(os.Stderr, "Password must be at least 8 characters\n")
			os.Exit(1)
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to hash password: %v\n", err)
			os.Exit(1)
		}

		if err := database.CreateAccount(&db.AccountData{
			Email:        email,
			LocalPart:    parts[0],
			Domain:       parts[1],
			PasswordHash: string(hash),
			IsAdmin:      false,
			IsActive:     true,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create account: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Account created: %s\n", email)

	case "list":
		var domain string
		if len(args) >= 2 {
			domain = args[1]
		}

		var accounts []*db.AccountData
		var err error

		if domain != "" {
			accounts, err = database.ListAccountsByDomain(domain)
		} else {
			// List all accounts
			domains, err := database.ListDomains()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to list domains: %v\n", err)
				os.Exit(1)
			}
			for _, d := range domains {
				domainAccounts, _ := database.ListAccountsByDomain(d.Name)
				accounts = append(accounts, domainAccounts...)
			}
		}

		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to list accounts: %v\n", err)
			os.Exit(1)
		}

		if len(accounts) == 0 {
			fmt.Println("No accounts found.")
			return
		}

		fmt.Println("Accounts:")
		fmt.Println("---------")
		for _, a := range accounts {
			status := "active"
			if !a.IsActive {
				status = "inactive"
			}
			admin := ""
			if a.IsAdmin {
				admin = "[admin]"
			}
			fmt.Printf("%-40s %s %s\n", a.Email, status, admin)
		}

	case "password":
		if len(args) < 2 {
			fmt.Println("Usage: umailserver account password <email>")
			os.Exit(1)
		}
		email := args[1]
		parts := strings.Split(email, "@")
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "Invalid email format: %s\n", email)
			os.Exit(1)
		}

		account, err := database.GetAccount(parts[1], parts[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Account not found: %s\n", email)
			os.Exit(1)
		}

		fmt.Print("Enter new password: ")
		password := readPassword()
		if len(password) < 8 {
			fmt.Fprintf(os.Stderr, "Password must be at least 8 characters\n")
			os.Exit(1)
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to hash password: %v\n", err)
			os.Exit(1)
		}

		account.PasswordHash = string(hash)
		account.UpdatedAt = time.Now()

		if err := database.UpdateAccount(account); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to update password: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Password updated for: %s\n", email)

	case "delete":
		if len(args) < 2 {
			fmt.Println("Usage: umailserver account delete <email>")
			os.Exit(1)
		}
		email := args[1]
		parts := strings.Split(email, "@")
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "Invalid email format: %s\n", email)
			os.Exit(1)
		}

		// Confirm deletion
		fmt.Printf("Are you sure you want to delete account %s? (y/N): ", email)
		var response string
		_, _ = fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Aborted.")
			os.Exit(0)
		}

		if err := database.DeleteAccount(parts[1], parts[0]); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to delete account: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Account deleted: %s\n", email)

	default:
		fmt.Fprintf(os.Stderr, "Unknown account subcommand: %s\n", subcmd)
		os.Exit(1)
	}
}

func cmdQueue(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: umailserver queue <subcommand>")
		fmt.Println("Subcommands: list, retry, flush, drop")
		os.Exit(1)
	}

	subcmd := args[0]

	// Open the configured backend (bbolt or postgres).
	database := openConfiguredStore()
	defer database.Close()

	switch subcmd {
	case "list":
		entries, err := database.GetPendingQueue(time.Now().Add(24 * time.Hour))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to list queue: %v\n", err)
			os.Exit(1)
		}
		if len(entries) == 0 {
			fmt.Println("Queue is empty.")
			return
		}
		fmt.Printf("%-20s %-30s %-30s %-10s %s\n", "ID", "From", "To", "Status", "Retries")
		fmt.Println(strings.Repeat("-", 110))
		for _, e := range entries {
			fmt.Printf("%-20s %-30s %-30s %-10s %d\n", e.ID, e.From, strings.Join(e.To, ","), e.Status, e.RetryCount)
		}
	case "retry":
		if len(args) < 2 {
			fmt.Println("Usage: umailserver queue retry <id>")
			os.Exit(1)
		}
		entry, err := database.GetQueueEntry(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Queue entry not found: %s\n", args[1])
			os.Exit(1)
		}
		entry.Status = "pending"
		entry.NextRetry = time.Now()
		entry.RetryCount = 0
		entry.LastError = ""
		if err := database.UpdateQueueEntry(entry); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to retry entry: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Queue entry retried: %s\n", args[1])
	case "flush":
		entries, err := database.GetPendingQueue(time.Now().Add(24 * time.Hour))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to flush queue: %v\n", err)
			os.Exit(1)
		}
		count := 0
		for _, e := range entries {
			if e.Status == "failed" {
				e.Status = "pending"
				e.NextRetry = time.Now()
				e.RetryCount = 0
				e.LastError = ""
				if err := database.UpdateQueueEntry(e); err == nil {
					count++
				}
			}
		}
		fmt.Printf("Flushed %d failed entries\n", count)
	case "drop":
		if len(args) < 2 {
			fmt.Println("Usage: umailserver queue drop <id>")
			os.Exit(1)
		}
		if err := database.Dequeue(args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to drop entry: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Queue entry dropped: %s\n", args[1])
	default:
		fmt.Fprintf(os.Stderr, "Unknown queue subcommand: %s\n", subcmd)
		os.Exit(1)
	}
}

func cmdCheck(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: umailserver check <type>")
		fmt.Println("Types: dns, tls, deliverability, mailbox")
		os.Exit(1)
	}

	checkType := args[0]

	// Mailbox consistency (mbck) reads the local canonical store rather than
	// running a network diagnostic, so it is handled before the diagnostics setup.
	if checkType == "mailbox" {
		rest := args[1:]
		if len(rest) < 1 {
			fmt.Println("Usage: umailserver check mailbox <email> [--repair]")
			os.Exit(1)
		}
		email := rest[0]
		fs := flag.NewFlagSet("check mailbox", flag.ExitOnError)
		repair := fs.Bool("repair", false, "fix detected inconsistencies (recreate missing semcore identities, delete orphan index/identity entries); run with the server stopped")
		if err := fs.Parse(rest[1:]); err != nil {
			os.Exit(1)
		}
		cmdCheckMailbox(email, *repair)
		return
	}

	// Load config
	cfg, err := loadCLIConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	diagnostics := cli.NewDiagnostics(cfg)

	switch checkType {
	case "dns":
		if len(args) < 2 {
			fmt.Println("Usage: umailserver check dns <domain>")
			os.Exit(1)
		}
		results, err := diagnostics.CheckDNS(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "DNS check failed: %v\n", err)
			os.Exit(1)
		}
		cli.PrintDNSResults(results)

	case "tls":
		if len(args) < 2 {
			fmt.Println("Usage: umailserver check tls <hostname>")
			os.Exit(1)
		}
		result, err := diagnostics.CheckTLS(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "TLS check failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("TLS Check: %s\n", result.Message)
		if result.Valid {
			fmt.Printf("  Protocol: %s\n", result.Protocol)
			fmt.Printf("  Version:  %s\n", result.Version)
			fmt.Printf("  Cipher:   %s\n", result.Cipher)
		}

	case "deliverability":
		if len(args) < 2 {
			fmt.Println("Usage: umailserver check deliverability <domain>")
			os.Exit(1)
		}
		result, err := diagnostics.CheckDeliverability(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Deliverability check failed: %v\n", err)
			os.Exit(1)
		}
		cli.PrintDeliverabilityResults(result)
		if result.OverallScore == "fail" {
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown check type: %s\n", checkType)
		os.Exit(1)
	}
}

// cmdCheckMailbox runs a consistency check (mbck) over a user's canonical store,
// reporting messages whose Maildir blob, IMAP index, and semcore identity
// disagree (the "ghost in EWS" class, dangling entries, etc.). When repair is
// true it then converges the stores: it recreates missing semcore identities for
// indexed messages (the EWS-ghost fix) and deletes orphan IMAP index and semcore
// identity entries whose blob is gone. Run with the server STOPPED (bbolt is
// single-writer). Exits non-zero when any inconsistency remains.
func cmdCheckMailbox(email string, repair bool) {
	localPart, domain, ok := strings.Cut(email, "@")
	if !ok || localPart == "" || domain == "" {
		fmt.Fprintf(os.Stderr, "check mailbox: invalid email %q\n", email)
		os.Exit(1)
	}
	cfg, err := loadCLIConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}
	// Open the canonical stores for the configured backend (bbolt or postgres).
	st, err := openMailCLIStores(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "check mailbox: %v\n", err)
		os.Exit(1)
	}
	defer st.close()
	if account, gerr := st.account.GetAccount(domain, localPart); gerr != nil || account == nil {
		fmt.Fprintf(os.Stderr, "check mailbox: account %s does not exist\n", email)
		os.Exit(1)
	}

	rep, err := mailcheck.Check(email,
		mailcheckIndex{db: st.index},
		mailcheckBlob{store: st.blob},
		mailcheckIdent{store: st.identity},
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "check mailbox: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("check mailbox %s: scanned %d IMAP index entries, %d semcore identities\n", email, rep.IndexCount, rep.SemcoreCount)
	if rep.Clean() {
		fmt.Println("OK: no inconsistencies found")
		return
	}
	fmt.Printf("FOUND %d inconsistency(ies):\n", len(rep.Issues))
	for _, iss := range rep.Issues {
		fmt.Printf("  %s\n", iss.String())
	}
	if !repair {
		os.Exit(1)
	}

	// --repair: converge the three stores, then re-check to confirm.
	rrep, rerr := mailcheck.Repair(email,
		mailcheckIndex{db: st.index},
		mailcheckBlob{store: st.blob},
		mailcheckRepairIdent{store: st.identity},
		mailcheckRepairer{pipe: st.pipe, index: st.index, ident: st.identity},
	)
	if rerr != nil {
		fmt.Fprintf(os.Stderr, "check mailbox --repair: %v\n", rerr)
		os.Exit(1)
	}
	fmt.Printf("repaired: recreated %d identity(ies), deleted %d orphan index entry(ies), %d orphan identity(ies)\n",
		rrep.Recreated, rrep.DeletedIndex, rrep.DeletedIdentity)
	for _, act := range rrep.Actions {
		fmt.Printf("  %s\n", act)
	}

	post, perr := mailcheck.Check(email,
		mailcheckIndex{db: st.index},
		mailcheckBlob{store: st.blob},
		mailcheckIdent{store: st.identity},
	)
	if perr != nil {
		fmt.Fprintf(os.Stderr, "check mailbox: re-check after repair: %v\n", perr)
		os.Exit(1)
	}
	if post.Clean() {
		fmt.Println("OK: mailbox consistent after repair")
		return
	}
	fmt.Printf("STILL %d inconsistency(ies) after repair:\n", len(post.Issues))
	for _, iss := range post.Issues {
		fmt.Printf("  %s\n", iss.String())
	}
	os.Exit(1)
}

// mailcheckIndex adapts the IMAP metadata index to mailcheck.IndexStore. The
// index is backend-agnostic (bbolt *storage.Database or *postgres.DB) behind
// imap.MetadataStore.
type mailcheckIndex struct{ db imap.MetadataStore }

func (a mailcheckIndex) ListMailboxes(user string) ([]string, error) { return a.db.ListMailboxes(user) }
func (a mailcheckIndex) GetMessageUIDs(user, mailbox string) ([]uint32, error) {
	return a.db.GetMessageUIDs(user, mailbox)
}
func (a mailcheckIndex) MessageID(user, mailbox string, uid uint32) (string, error) {
	meta, err := a.db.GetMessageMetadata(user, mailbox, uid)
	if err != nil {
		return "", err
	}
	return meta.MessageID, nil
}

// mailcheckBlob adapts the Maildir blob store to mailcheck.BlobStore (and, via
// ReadMessage, mailcheck.RepairBlob).
type mailcheckBlob struct{ store *storage.MessageStore }

func (a mailcheckBlob) MessageExists(user, id string) bool { return a.store.MessageExists(user, id) }
func (a mailcheckBlob) ReadMessage(user, id string) ([]byte, error) {
	return a.store.ReadMessage(user, id)
}

// mailcheckIdent adapts the semcore identity store to mailcheck.IdentityStore.
// Folder identities are keyed by the raw email (the server/deliverLocal
// convention), so that is what is passed to ListFolderIdentitiesForMailbox. The
// store is backend-agnostic (bbolt *semcore.BoltIdentityStore or *postgres.DB)
// behind ews.IdentityStore.
type mailcheckIdent struct{ store ews.IdentityStore }

func (a mailcheckIdent) MailboxKey(email string) (string, bool, error) {
	if _, err := a.store.GetMailboxIDByEmail(email); err != nil {
		if errors.Is(err, semcore.ErrMailboxNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return email, true, nil
}
func (a mailcheckIdent) FolderIDs(mboxKey string) ([]string, error) {
	folders, err := a.store.ListFolderIdentitiesForMailbox(mboxKey)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(folders))
	for i, f := range folders {
		out[i] = f.FolderID.String()
	}
	return out, nil
}
func (a mailcheckIdent) ItemKeys(folderID string) ([]string, error) {
	fid, err := semcore.NewFolderId(folderID)
	if err != nil {
		return nil, err
	}
	items, err := a.store.ListItemIdentitiesByFolder(fid)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.MsgKey)
	}
	return out, nil
}

// mailcheckRepairIdent adapts the semcore identity store to
// mailcheck.RepairIdentity. Unlike the read-only adapter, it groups items by the
// stored IMAP folder NAME (resolved via FolderNameByID) so the repair cross-check
// is per-folder: a message must have an identity in the same folder it is indexed
// under. Backend-agnostic behind ews.IdentityStore.
type mailcheckRepairIdent struct{ store ews.IdentityStore }

func (a mailcheckRepairIdent) MailboxKey(email string) (string, bool, error) {
	if _, err := a.store.GetMailboxIDByEmail(email); err != nil {
		if errors.Is(err, semcore.ErrMailboxNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return email, true, nil
}

func (a mailcheckRepairIdent) FolderItems(mboxKey string) (map[string][]mailcheck.ItemRef, error) {
	folders, err := a.store.ListFolderIdentitiesForMailbox(mboxKey)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]mailcheck.ItemRef, len(folders))
	for _, f := range folders {
		name, nerr := a.store.FolderNameByID(mboxKey, f.FolderID)
		if nerr != nil {
			return nil, nerr
		}
		items, ierr := a.store.ListItemIdentitiesByFolder(f.FolderID)
		if ierr != nil {
			return nil, ierr
		}
		refs := make([]mailcheck.ItemRef, 0, len(items))
		for _, it := range items {
			refs = append(refs, mailcheck.ItemRef{ItemID: it.ItemID.String(), MsgKey: it.MsgKey})
		}
		out[name] = refs
	}
	return out, nil
}

// mailcheckRepairer adapts the canonical write paths to mailcheck.Repairer.
// RecreateIdentity files the semcore-only layer for a message whose blob and IMAP
// index already exist (the EWS-ghost fix), mirroring step 1 of mailImporter.fileOne.
type mailcheckRepairer struct {
	pipe  *semcore.MutationPipeline
	index imap.MetadataStore // bbolt *storage.Database or *postgres.DB
	ident ews.IdentityStore  // bbolt *semcore.BoltIdentityStore or *postgres.DB
}

func (w mailcheckRepairer) RecreateIdentity(email, mailbox string, raw []byte) error {
	mboxID, err := w.ident.EnsureMailboxId(email)
	if err != nil {
		return fmt.Errorf("ensure mailbox identity: %w", err)
	}
	fldID, err := w.ident.EnsureFolderId(email, mailbox, folderRole(mailbox))
	if err != nil {
		return fmt.Errorf("ensure folder identity: %w", err)
	}
	if _, err := w.pipe.MutateItem(&semcore.MutationInput{
		MailboxID:    mboxID,
		FolderID:     fldID,
		RawMessage:   raw,
		InternalDate: messageDate(raw),
		Actor:        email,
		Email:        email,
		Source:       semcore.MutationSourceAPI,
		IsRead:       true,
	}); err != nil {
		return fmt.Errorf("semcore mutate: %w", err)
	}
	return nil
}

func (w mailcheckRepairer) DeleteIndexEntry(email, mailbox string, uid uint32) error {
	return w.index.DeleteMessage(email, mailbox, uid)
}

func (w mailcheckRepairer) DeleteIdentity(itemID string) error {
	id, err := semcore.NewItemId(itemID)
	if err != nil {
		return fmt.Errorf("parse item id %q: %w", itemID, err)
	}
	return w.ident.DeleteItemIdentity(id)
}

// folderSize is one mailbox folder's message count and total byte size.
type folderSize struct {
	name  string
	count int
	bytes int64
}

// mailboxSizes sums each folder's message sizes from the IMAP index (meta.Size
// is the stored RFC822 size that quota counts), returning per-folder figures
// plus the grand total. Read-only; works on either backend.
func mailboxSizes(index imap.MetadataStore, email string) (folders []folderSize, totalBytes int64, totalCount int, err error) {
	names, err := index.ListMailboxes(email)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("list mailboxes: %w", err)
	}
	sort.Strings(names)
	for _, name := range names {
		uids, uerr := index.GetMessageUIDs(email, name)
		if uerr != nil {
			return nil, 0, 0, fmt.Errorf("uids for %q: %w", name, uerr)
		}
		fs := folderSize{name: name}
		for _, uid := range uids {
			meta, merr := index.GetMessageMetadata(email, name, uid)
			if merr != nil || meta == nil {
				continue
			}
			fs.count++
			fs.bytes += meta.Size
		}
		folders = append(folders, fs)
		totalBytes += fs.bytes
		totalCount += fs.count
	}
	return folders, totalBytes, totalCount, nil
}

// effectiveQuota returns the binding quota limit for an account: the smaller of
// the account limit and the domain ceiling, with 0 meaning unlimited on either
// side (mirrors db.IncrementQuota). dom may be nil.
func effectiveQuota(account *db.AccountData, dom *db.DomainData) int64 {
	limit := account.QuotaLimit
	if dom != nil && dom.MaxMailboxSize > 0 && (limit == 0 || dom.MaxMailboxSize < limit) {
		limit = dom.MaxMailboxSize
	}
	return limit
}

// humanBytes formats a byte count as a human-readable size (binary units).
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// cmdMbsize reports mailbox storage size: per-folder message count and bytes for
// a single mailbox (plus the QuotaUsed counter vs the computed size and the
// effective quota), or a one-line-per-account summary for a whole domain / all
// accounts. It is a read-only per-mailbox storage size report. Run with the server
// STOPPED on bbolt (single-writer); postgres runs concurrently.
func cmdMbsize(args []string) {
	fs := flag.NewFlagSet("mbsize", flag.ExitOnError)
	all := fs.Bool("all", false, "Report every account in every domain")
	domainFlag := fs.String("domain", "", "Report every account in this domain")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}
	email := fs.Arg(0)
	if !*all && *domainFlag == "" && email == "" {
		fmt.Fprintln(os.Stderr, "Usage: umailserver mbsize <email> | --domain <domain> | --all")
		os.Exit(1)
	}

	cfg, err := loadCLIConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}
	st, err := openMailCLIStores(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mbsize: %v\n", err)
		os.Exit(1)
	}
	defer st.close()

	if *all || *domainFlag != "" {
		mbsizeBulk(st, *domainFlag)
		return
	}

	localPart, domain, ok := strings.Cut(email, "@")
	if !ok || localPart == "" || domain == "" {
		fmt.Fprintf(os.Stderr, "mbsize: invalid email %q\n", email)
		os.Exit(1)
	}
	account, gerr := st.account.GetAccount(domain, localPart)
	if gerr != nil || account == nil {
		fmt.Fprintf(os.Stderr, "mbsize: account %s does not exist\n", email)
		os.Exit(1)
	}
	folders, totalBytes, totalCount, serr := mailboxSizes(st.index, email)
	if serr != nil {
		fmt.Fprintf(os.Stderr, "mbsize: %v\n", serr)
		os.Exit(1)
	}

	fmt.Printf("mailbox %s\n", email)
	for _, f := range folders {
		fmt.Printf("  %-24s %6d msg  %12s\n", f.name, f.count, humanBytes(f.bytes))
	}
	fmt.Printf("  %-24s %6d msg  %12s\n", "TOTAL", totalCount, humanBytes(totalBytes))

	dom, _ := st.account.GetDomain(domain) //nolint:errcheck // absent domain -> nil, no ceiling
	limit := effectiveQuota(account, dom)
	limitStr := "unlimited"
	pct := ""
	if limit > 0 {
		limitStr = humanBytes(limit)
		pct = fmt.Sprintf(" (%.1f%%)", 100*float64(totalBytes)/float64(limit))
	}
	fmt.Printf("  quota: computed %s / effective %s%s\n", humanBytes(totalBytes), limitStr, pct)
	// The QuotaUsed counter is only bumped at inbound/local delivery (not IMAP
	// APPEND / EWS / JMAP writes), so it can drift from the real size — surface it.
	if account.QuotaUsed != totalBytes {
		fmt.Printf("  NOTE: QuotaUsed counter is %s, drifts from computed %s by %s\n",
			humanBytes(account.QuotaUsed), humanBytes(totalBytes), humanBytes(absInt64(account.QuotaUsed-totalBytes)))
	}
}

// absInt64 returns the absolute value of n.
func absInt64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// mbsizeBulk prints a one-line-per-account size summary for a single domain (when
// domain != "") or every domain, plus a grand total.
func mbsizeBulk(st *mailCLIStores, domain string) {
	var domains []string
	if domain != "" {
		domains = []string{domain}
	} else {
		doms, err := st.account.ListDomains()
		if err != nil {
			fmt.Fprintf(os.Stderr, "mbsize: list domains: %v\n", err)
			os.Exit(1)
		}
		for _, d := range doms {
			domains = append(domains, d.Name)
		}
		sort.Strings(domains)
	}

	fmt.Printf("%-36s %8s %14s %16s\n", "MAILBOX", "MSGS", "SIZE", "QUOTA(used/eff)")
	var grandBytes int64
	var grandCount, accounts int
	for _, dn := range domains {
		accts, err := st.account.ListAccountsByDomain(dn)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mbsize: list accounts for %q: %v\n", dn, err)
			os.Exit(1)
		}
		dom, _ := st.account.GetDomain(dn) //nolint:errcheck // absent -> nil, no ceiling
		for _, a := range accts {
			_, totalBytes, totalCount, serr := mailboxSizes(st.index, a.Email)
			if serr != nil {
				fmt.Fprintf(os.Stderr, "mbsize: %s: %v\n", a.Email, serr)
				os.Exit(1)
			}
			limit := effectiveQuota(a, dom)
			limitStr := "unlimited"
			if limit > 0 {
				limitStr = humanBytes(limit)
			}
			fmt.Printf("%-36s %8d %14s %16s\n", a.Email, totalCount, humanBytes(totalBytes),
				humanBytes(a.QuotaUsed)+" / "+limitStr)
			grandBytes += totalBytes
			grandCount += totalCount
			accounts++
		}
	}
	fmt.Printf("total: %d account(s), %d msg, %s\n", accounts, grandCount, humanBytes(grandBytes))
}

func cmdTest(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: umailserver test <type>")
		fmt.Println("Types: send")
		os.Exit(1)
	}

	testType := args[0]
	fmt.Printf("Test command: %s\n", testType)

	switch testType {
	case "send":
		if len(args) < 4 {
			fmt.Println("Usage: umailserver test send <from> <to> <subject>")
			os.Exit(1)
		}
		fmt.Printf("Sending test email from %s to %s\n", args[1], args[2])
	default:
		fmt.Fprintf(os.Stderr, "Unknown test type: %s\n", testType)
		os.Exit(1)
	}
}

func cmdBackup(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: umailserver backup <path>")
		os.Exit(1)
	}

	backupPath := args[0]

	// Load config
	cfg, err := loadCLIConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	bm := cli.NewBackupManager(cfg)
	if err := bm.Backup(backupPath); err != nil {
		fmt.Fprintf(os.Stderr, "Backup failed: %v\n", err)
		os.Exit(1)
	}
}

func cmdRestore(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: umailserver restore <backup-file>")
		os.Exit(1)
	}

	backupFile := args[0]

	// Load config
	cfg, err := loadCLIConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	bm := cli.NewBackupManager(cfg)
	if err := bm.Restore(backupFile); err != nil {
		fmt.Fprintf(os.Stderr, "Restore failed: %v\n", err)
		os.Exit(1)
	}
}

// migrateBboltToPostgres copies the whole bbolt deployment (account/metadata
// store, message metadata, and the semantic core) into the PostgreSQL database
// at dsn. The server must be stopped (bbolt is single-writer) and the target
// must be empty. Maildir message bodies stay on disk.
func migrateBboltToPostgres(dsn string) {
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "Usage: umailserver migrate --type postgres --postgres-dsn <dsn>")
		fmt.Fprintln(os.Stderr, "Run with the server stopped; the target database must be empty.")
		os.Exit(1)
	}
	cfg, err := loadCLIConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	dst, err := postgres.Open(ctx, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open target PostgreSQL: %v\n", err)
		os.Exit(1)
	}
	defer dst.Close() //nolint:errcheck // process is exiting
	if err := dst.Migrate(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to apply PostgreSQL schema: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Migrating bbolt → PostgreSQL (server must be stopped)...")
	report, err := migratestore.Migrate(cfg.DatabasePath(), cfg.Server.DataDir, dst)
	if err != nil {
		// Print the partial report so the operator sees how far it got.
		fmt.Fprintf(os.Stderr, "Migration failed: %v\n", err)
		printMigrateReport(report)
		os.Exit(1)
	}
	fmt.Println("Migration complete.")
	printMigrateReport(report)
}

// printMigrateReport prints the per-record-type counts of a store migration.
func printMigrateReport(r *migratestore.Report) {
	if r == nil {
		return
	}
	fmt.Printf("  Tenants: %d  Domains: %d  Accounts: %d  Aliases: %d  MailGroups: %d\n",
		r.Tenants, r.Domains, r.Accounts, r.Aliases, r.MailGroups)
	fmt.Printf("  Prefs: %d  Signatures: %d  Categories: %d  Vacations: %d  UserConfigs: %d\n",
		r.UIPrefs, r.Signatures, r.Categories, r.Vacations, r.UserConfigs)
	fmt.Printf("  Mailboxes: %d  Messages: %d  Subscriptions: %d  ACLs: %d  Threads: %d\n",
		r.Mailboxes, r.Messages, r.Subscriptions, r.ACLs, r.Threads)
	fmt.Printf("  MailboxIDs: %d  FolderIDs: %d  ItemIDs: %d  Conversations: %d\n",
		r.MailboxIdentities, r.FolderIdentities, r.ItemIdentities, r.Conversations)
	fmt.Printf("  Rules: %d  OOF: %d  Resources: %d  RoomLists: %d  Delegates: %d\n",
		r.Rules, r.OOFPolicies, r.Resources, r.RoomLists, r.Delegates)
	fmt.Printf("  CalendarItems: %d  Tasks: %d  Contacts: %d\n",
		r.CalendarItems, r.Tasks, r.Contacts)
}

func cmdMigrate(args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	sourceType := fs.String("type", "", "Source type (imap, dovecot, mbox, dav, postgres)")
	source := fs.String("source", "", "Source path or URL")
	username := fs.String("username", "", "Source username (for IMAP)")
	password := fs.String("password", "", "Source password (for IMAP)")
	targetUser := fs.String("target", "", "Target user email")
	dryRun := fs.Bool("dry-run", false, "Dry run mode")
	passwdFile := fs.String("passwd-file", "", "Password file (for Dovecot)")
	postgresDSN := fs.String("postgres-dsn", "", "Target PostgreSQL DSN (for --type postgres)")

	_ = fs.Parse(args)

	// bbolt → PostgreSQL store migration: copy the entire bbolt deployment
	// (accounts/metadata, mailbox/message metadata, and the semantic core) into a
	// fresh PostgreSQL database. Maildir bodies stay on disk. Run with the server
	// STOPPED — the bbolt stores are single-writer — and against an EMPTY target.
	if *sourceType == "postgres" {
		migrateBboltToPostgres(*postgresDSN)
		return
	}

	// DAV unification migration: import legacy filesystem CalDAV/CardDAV data
	// into the canonical semcore collaboration store. Run with the server
	// stopped (the Bolt store is single-writer). Uses the config data dir.
	if *sourceType == "dav" {
		cfg, err := loadCLIConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
			os.Exit(1)
		}
		if err := migrateDAVToCollab(cfg.Server.DataDir, *dryRun); err != nil {
			fmt.Fprintf(os.Stderr, "DAV migration failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *sourceType == "" || *source == "" {
		fmt.Println("Usage: umailserver migrate --type <type> --source <source>")
		fmt.Println("Types: imap, dovecot, mbox, dav")
		fmt.Println("\nExamples:")
		fmt.Println("  umailserver migrate --type imap --source imaps://oldserver.com --username user@old.com --target user@new.com")
		fmt.Println("  umailserver migrate --type dovecot --source /var/mail --passwd-file /etc/dovecot/users")
		fmt.Println("  umailserver migrate --type mbox --source /path/to/mail/*.mbox")
		os.Exit(1)
	}

	// Load config and database
	cfg, err := loadCLIConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// The legacy importer (IMAP/Dovecot/mbox) writes through the concrete bbolt
	// *db.DB, so it cannot target the PostgreSQL backend.
	if cfg.DatabaseBackend() == "postgres" {
		fmt.Fprintln(os.Stderr, "legacy migration (imap/dovecot/mbox) supports only the bbolt backend; database.backend is \"postgres\".")
		os.Exit(1)
	}

	database, err := db.Open(cfg.DatabasePath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	msgStore, err := storage.NewMessageStore(cfg.Server.DataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open message store: %v\n", err)
		os.Exit(1)
	}

	mm := cli.NewMigrationManager(database, msgStore, nil)

	opts := cli.MigrateOptions{
		SourceType: *sourceType,
		SourceURL:  *source,
		SourcePath: *source,
		Username:   *username,
		Password:   *password,
		TargetUser: *targetUser,
		DryRun:     *dryRun,
	}

	switch *sourceType {
	case "imap":
		if err := mm.MigrateFromIMAP(opts); err != nil {
			fmt.Fprintf(os.Stderr, "IMAP migration failed: %v\n", err)
			os.Exit(1)
		}
	case "dovecot":
		if err := mm.MigrateFromDovecot(*source, *passwdFile); err != nil {
			fmt.Fprintf(os.Stderr, "Dovecot migration failed: %v\n", err)
			os.Exit(1)
		}
	case "mbox":
		if err := mm.MigrateFromMBOX(*source); err != nil {
			fmt.Fprintf(os.Stderr, "MBOX migration failed: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown source type: %s\n", *sourceType)
		os.Exit(1)
	}
}

// cmdImport bulk-imports messages from an mbox file, a .eml file/directory, or a
// Maildir tree into a user's mailbox, writing the canonical store (Maildir blob
// + IMAP index + semcore identity) so the messages are visible across IMAP,
// POP3, JMAP, EWS, and webmail. Run with the server STOPPED — the bbolt stores
// are single-writer. Re-running is idempotent: a message whose content already
// exists in the target folder is skipped.
func cmdImport(args []string) {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	user := fs.String("user", "", "Target user email (required)")
	mboxPath := fs.String("mbox", "", "Path to an mbox file")
	emlPath := fs.String("eml", "", "Path to a .eml file or a directory of .eml files")
	maildirPath := fs.String("maildir", "", "Path to a Maildir tree")
	icsPath := fs.String("ics", "", "Path to an iCalendar .ics file or a directory of .ics files (calendar events)")
	vcfPath := fs.String("vcf", "", "Path to a vCard .vcf file or a directory of .vcf files (contacts)")
	folder := fs.String("folder", "INBOX", "Target folder for sources without their own (mbox/.eml)")
	dryRun := fs.Bool("dry-run", false, "Parse and report the object count without writing")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	if *user == "" {
		fmt.Fprintln(os.Stderr, "import: --user <email> is required")
		os.Exit(1)
	}
	localPart, domain, ok := strings.Cut(*user, "@")
	if !ok || localPart == "" || domain == "" {
		fmt.Fprintf(os.Stderr, "import: invalid email %q\n", *user)
		os.Exit(1)
	}
	srcCount := 0
	for _, s := range []string{*mboxPath, *emlPath, *maildirPath, *icsPath, *vcfPath} {
		if s != "" {
			srcCount++
		}
	}
	if srcCount != 1 {
		fmt.Fprintln(os.Stderr, "import: provide exactly one of --mbox, --eml, --maildir, --ics, or --vcf")
		os.Exit(1)
	}

	cfg, err := loadCLIConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Calendar (.ics) and contacts (.vcf) file into the canonical collaboration
	// store, not the mail index — a separate path.
	if *icsPath != "" || *vcfPath != "" {
		cmdImportPIM(cfg, localPart+"@"+domain, localPart, domain, *icsPath, *vcfPath, *dryRun)
		return
	}

	// Parse the source BEFORE opening (locking) the stores.
	messages, err := parseImportSource(*mboxPath, *emlPath, *maildirPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "import: %v\n", err)
		os.Exit(1)
	}
	if len(messages) == 0 {
		fmt.Println("import: no messages found in source")
		return
	}
	if *dryRun {
		fmt.Printf("import: %d message(s) would be imported into %s\n", len(messages), *user)
		return
	}

	// Open the canonical stores for the configured backend (bbolt or postgres).
	st, err := openMailCLIStores(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "import: %v\n", err)
		os.Exit(1)
	}
	defer st.close()

	if account, gerr := st.account.GetAccount(domain, localPart); gerr != nil || account == nil {
		fmt.Fprintf(os.Stderr, "import: account %s does not exist\n", *user)
		os.Exit(1)
	}

	imp := &mailImporter{
		email:         localPart + "@" + domain,
		defaultFolder: *folder,
		msgStore:      st.blob,
		index:         st.index,
		pipe:          st.pipe,
		seen:          map[string]map[string]bool{},
	}
	imported, skipped, failed := imp.run(messages)
	fmt.Printf("import: %d imported, %d skipped (duplicate), %d failed into %s\n", imported, skipped, failed, imp.email)
	fmt.Println("Note: full-text search indexes imported mail on the next server start (the index rebuilds from the canonical store).")
	if failed > 0 {
		os.Exit(1)
	}
}

// closeImportStore closes a store opened by cmdImport, reporting (not hiding) a
// close error.
func closeImportStore(name string, closer interface{ Close() error }) {
	if err := closer.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "import: close %s: %v\n", name, err)
	}
}

// parseImportSource dispatches to the right parser for whichever source flag is
// set. The .eml flag accepts a single file or a directory of .eml files.
func parseImportSource(mboxPath, emlPath, maildirPath string) ([]mailimport.Message, error) {
	switch {
	case mboxPath != "":
		f, err := os.Open(filepath.Clean(mboxPath))
		if err != nil {
			return nil, fmt.Errorf("open mbox: %w", err)
		}
		defer closeImportStore("mbox file", f)
		return mailimport.ReadMbox(f)
	case emlPath != "":
		info, err := os.Stat(emlPath)
		if err != nil {
			return nil, fmt.Errorf("stat eml: %w", err)
		}
		if info.IsDir() {
			return mailimport.ReadEMLDir(emlPath)
		}
		m, err := mailimport.ReadEMLFile(emlPath)
		if err != nil {
			return nil, err
		}
		return []mailimport.Message{m}, nil
	default:
		return mailimport.ReadMaildir(maildirPath)
	}
}

// readPIMFiles reads a single .ics/.vcf file OR every matching file in a
// directory, returning each file's raw bytes (the caller parses per type). A
// directory lets a folder of single-object exports import in one run.
func readPIMFiles(path, ext string) ([][]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", ext, err)
	}
	if !info.IsDir() {
		data, rerr := os.ReadFile(filepath.Clean(path))
		if rerr != nil {
			return nil, fmt.Errorf("read %s: %w", ext, rerr)
		}
		return [][]byte{data}, nil
	}
	entries, derr := os.ReadDir(path)
	if derr != nil {
		return nil, fmt.Errorf("read dir: %w", derr)
	}
	var out [][]byte
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ext) {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(path, e.Name()))
		if rerr != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), rerr)
		}
		out = append(out, data)
	}
	return out, nil
}

// cmdImportPIM dispatches calendar/task (.ics) vs contact (.vcf) import; both
// file into the canonical collaboration store the SAME way CalDAV/CardDAV/EWS/
// webmail write them (keyed by iCal/vCard UID), so imported PIM data is
// cross-protocol-visible.
func cmdImportPIM(cfg *config.Config, email, localPart, domain, icsPath, vcfPath string, dryRun bool) {
	if icsPath != "" {
		cmdImportICS(cfg, email, localPart, domain, icsPath, dryRun)
		return
	}
	cmdImportVCF(cfg, email, localPart, domain, vcfPath, dryRun)
}

// filePIMEvents files iCal components into a calendar OR task store (both satisfy
// caldav.Store), skipping any whose UID already exists (idempotent re-import).
func filePIMEvents(store caldav.Store, email, label string, comps []pimport.Component) (imported, skipped, failed int) {
	for _, c := range comps {
		if ex, gerr := store.GetEvent(email, "default", c.UID); gerr == nil && ex != "" {
			skipped++
			continue
		}
		if err := store.SaveEvent(email, "default", &caldav.CalendarEvent{UID: c.UID}, c.Raw); err != nil {
			fmt.Fprintf(os.Stderr, "import: save %s %s: %v\n", label, c.UID, err)
			failed++
			continue
		}
		imported++
	}
	return imported, skipped, failed
}

// cmdImportICS files a .ics file/dir: VEVENTs into the calendar store and VTODOs
// into the tasks store. A single iCal export may carry both.
func cmdImportICS(cfg *config.Config, email, localPart, domain, icsPath string, dryRun bool) {
	blobs, err := readPIMFiles(icsPath, ".ics")
	if err != nil {
		fmt.Fprintf(os.Stderr, "import: %v\n", err)
		os.Exit(1)
	}
	var events, todos []pimport.Component
	for _, b := range blobs {
		ev, td, perr := pimport.ReadICS(b)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "import: parse ics: %v\n", perr)
			os.Exit(1)
		}
		events = append(events, ev...)
		todos = append(todos, td...)
	}
	if len(events) == 0 && len(todos) == 0 {
		fmt.Println("import: no calendar events or tasks found in source")
		return
	}
	if dryRun {
		fmt.Printf("import: %d event(s) + %d task(s) would be imported into %s\n", len(events), len(todos), email)
		return
	}

	st, err := openMailCLIStores(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "import: %v\n", err)
		os.Exit(1)
	}
	defer st.close()
	if account, gerr := st.account.GetAccount(domain, localPart); gerr != nil || account == nil {
		fmt.Fprintf(os.Stderr, "import: account %s does not exist\n", email)
		os.Exit(1)
	}

	ei, es, ef := filePIMEvents(st.cal, email, "event", events)
	ti, ts, tf := filePIMEvents(st.task, email, "task", todos)
	fmt.Printf("import: events %d imported / %d skipped, tasks %d imported / %d skipped, %d failed into %s\n",
		ei, es, ti, ts, ef+tf, email)
	if ef+tf > 0 {
		os.Exit(1)
	}
}

// cmdImportVCF files a .vcf file/dir of contacts into the contacts store.
func cmdImportVCF(cfg *config.Config, email, localPart, domain, vcfPath string, dryRun bool) {
	blobs, err := readPIMFiles(vcfPath, ".vcf")
	if err != nil {
		fmt.Fprintf(os.Stderr, "import: %v\n", err)
		os.Exit(1)
	}
	var cards []pimport.Component
	for _, b := range blobs {
		c, perr := pimport.ReadVCF(b)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "import: parse vcf: %v\n", perr)
			os.Exit(1)
		}
		cards = append(cards, c...)
	}
	if len(cards) == 0 {
		fmt.Println("import: no contacts found in source")
		return
	}
	if dryRun {
		fmt.Printf("import: %d contact(s) would be imported into %s\n", len(cards), email)
		return
	}

	st, err := openMailCLIStores(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "import: %v\n", err)
		os.Exit(1)
	}
	defer st.close()
	if account, gerr := st.account.GetAccount(domain, localPart); gerr != nil || account == nil {
		fmt.Fprintf(os.Stderr, "import: account %s does not exist\n", email)
		os.Exit(1)
	}

	imported, skipped, failed := 0, 0, 0
	for _, c := range cards {
		if ex, gerr := st.card.GetContact(email, "default", c.UID); gerr == nil && ex != "" {
			skipped++
			continue
		}
		if err := st.card.SaveContact(email, "default", &carddav.Contact{UID: c.UID}, c.Raw); err != nil {
			fmt.Fprintf(os.Stderr, "import: save contact %s: %v\n", c.UID, err)
			failed++
			continue
		}
		imported++
	}
	fmt.Printf("import: %d imported, %d skipped (duplicate UID), %d failed into %s\n", imported, skipped, failed, email)
	if failed > 0 {
		os.Exit(1)
	}
}

// mailImporter files parsed messages into the canonical store with content-hash
// deduplication per folder.
type mailImporter struct {
	email         string
	defaultFolder string
	msgStore      *storage.MessageStore
	index         imap.MetadataStore // bbolt *storage.Database or *postgres.DB
	pipe          *semcore.MutationPipeline
	seen          map[string]map[string]bool // folder -> set of content-hash IDs already present
}

// run files every message, returning counts of imported, skipped (duplicate),
// and failed.
func (m *mailImporter) run(messages []mailimport.Message) (imported, skipped, failed int) {
	for _, msg := range messages {
		folder := msg.Folder
		if folder == "" {
			folder = m.defaultFolder
		}
		raw := mailimport.NormalizeCRLF(msg.Raw)
		hash := sha256.Sum256(raw)
		id := hex.EncodeToString(hash[:])
		if m.existing(folder)[id] {
			skipped++
			continue
		}
		if err := m.fileOne(folder, raw); err != nil {
			fmt.Fprintf(os.Stderr, "import: file message into %s: %v\n", folder, err)
			failed++
			continue
		}
		m.seen[folder][id] = true
		imported++
	}
	return imported, skipped, failed
}

// existing returns (loading once) the set of content-hash message IDs already in
// a folder, so a re-run skips messages already imported.
func (m *mailImporter) existing(folder string) map[string]bool {
	if set, ok := m.seen[folder]; ok {
		return set
	}
	set := map[string]bool{}
	if uids, err := m.index.GetMessageUIDs(m.email, folder); err == nil {
		for _, uid := range uids {
			if meta, merr := m.index.GetMessageMetadata(m.email, folder, uid); merr == nil {
				set[meta.MessageID] = true
			}
		}
	}
	m.seen[folder] = set
	return set
}

// fileOne writes one message to all three canonical layers: semcore identity
// (EWS visibility), the Maildir blob, and the IMAP index. The three writes
// mirror server.deliverLocal (minus quota/forwarding/notifications, which do not
// apply to an offline bulk import).
func (m *mailImporter) fileOne(folder string, raw []byte) error {
	internalDate := messageDate(raw)

	// 1. Semcore identity — EWS FindItem reads this, not the IMAP index.
	mboxID, err := m.pipe.Identity().EnsureMailboxId(m.email)
	if err != nil {
		return fmt.Errorf("ensure mailbox identity: %w", err)
	}
	fldID, err := m.pipe.Identity().EnsureFolderId(m.email, folder, folderRole(folder))
	if err != nil {
		return fmt.Errorf("ensure folder identity: %w", err)
	}
	if _, err := m.pipe.MutateItem(&semcore.MutationInput{
		MailboxID:    mboxID,
		FolderID:     fldID,
		RawMessage:   raw,
		InternalDate: internalDate,
		Actor:        m.email,
		Email:        m.email,
		Source:       semcore.MutationSourceAPI,
		IsRead:       true,
	}); err != nil {
		return fmt.Errorf("semcore mutate: %w", err)
	}

	// 2. Maildir blob (idempotent by content hash; messageID == contentID).
	messageID, err := m.msgStore.StoreMessage(m.email, raw)
	if err != nil {
		return fmt.Errorf("store blob: %w", err)
	}

	// 3. IMAP metadata index.
	if err := m.index.CreateMailbox(m.email, folder); err != nil {
		return fmt.Errorf("ensure mailbox %q: %w", folder, err)
	}
	uid, err := m.index.GetNextUID(m.email, folder)
	if err != nil {
		return fmt.Errorf("next uid: %w", err)
	}
	hdr := messageHeaders(raw)
	threadID, _ := m.index.GetOrCreateThreadID(m.email, folder, hdr.subject, hdr.messageID, hdr.inReplyTo, hdr.references) //nolint:errcheck
	meta := &storage.MessageMetadata{
		MessageID:    messageID,
		UID:          uid,
		Flags:        []string{"\\Seen"},
		InternalDate: internalDate,
		Size:         int64(len(raw)),
		Subject:      hdr.subject,
		Date:         hdr.date,
		From:         hdr.from,
		To:           hdr.to,
		ThreadID:     threadID,
		InReplyTo:    hdr.inReplyTo,
		References:   hdr.references,
	}
	if err := m.index.StoreMessageMetadata(m.email, folder, uid, meta); err != nil {
		return fmt.Errorf("store metadata: %w", err)
	}
	return nil
}

// importHeaders holds the header fields the IMAP index records.
type importHeaders struct {
	subject, from, to, date, messageID, inReplyTo string
	references                                    []string
}

// messageHeaders parses the header fields needed for the IMAP index.
func messageHeaders(raw []byte) importHeaders {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return importHeaders{}
	}
	return importHeaders{
		subject:    msg.Header.Get("Subject"),
		from:       msg.Header.Get("From"),
		to:         msg.Header.Get("To"),
		date:       msg.Header.Get("Date"),
		messageID:  msg.Header.Get("Message-ID"),
		inReplyTo:  msg.Header.Get("In-Reply-To"),
		references: strings.Fields(msg.Header.Get("References")),
	}
}

// messageDate returns the message's Date header as the internal date, falling
// back to now when it is missing or unparseable, so imported mail sorts by its
// original time.
func messageDate(raw []byte) time.Time {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return time.Now()
	}
	if d, derr := mail.ParseDate(msg.Header.Get("Date")); derr == nil {
		return d
	}
	return time.Now()
}

// folderRole maps a folder name to its distinguished role by inverting the
// canonical role->name map, so a standard target (e.g. INBOX) converges on the
// same semcore folder the server uses; a custom folder yields "" (no role).
func folderRole(name string) string {
	for _, role := range []string{"inbox", "sent", "drafts", "junk", "trash", "archive", "notes", "scheduled"} {
		if strings.EqualFold(semcore.CanonicalFolderNameForRole(role), name) {
			return role
		}
	}
	return ""
}

// cmdExport reads a user's messages from the canonical store and writes them to
// a standard interchange format: a single mbox file, one .eml per message under
// a directory, or a Maildir tree. It is the inverse of `umailserver import` and
// reuses the canonical READ path (no semcore needed for reads). Run with the
// server STOPPED (bbolt is single-writer).
func cmdExport(args []string) {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	user := fs.String("user", "", "Source user email (required)")
	mboxPath := fs.String("mbox", "", "Write all messages to this mbox file (flat)")
	emlDir := fs.String("eml", "", "Write one .eml per message under this directory (folder subdirs)")
	maildirDir := fs.String("maildir", "", "Write a Maildir tree to this directory (folders preserved)")
	icsPath := fs.String("ics", "", "Write all calendar events to this iCalendar .ics file")
	vcfPath := fs.String("vcf", "", "Write all contacts to this vCard .vcf file")
	folder := fs.String("folder", "", "Export only this folder (default: every mailbox)")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	if *user == "" {
		fmt.Fprintln(os.Stderr, "export: --user <email> is required")
		os.Exit(1)
	}
	localPart, domain, ok := strings.Cut(*user, "@")
	if !ok || localPart == "" || domain == "" {
		fmt.Fprintf(os.Stderr, "export: invalid email %q\n", *user)
		os.Exit(1)
	}
	dstCount := 0
	for _, s := range []string{*mboxPath, *emlDir, *maildirDir, *icsPath, *vcfPath} {
		if s != "" {
			dstCount++
		}
	}
	if dstCount != 1 {
		fmt.Fprintln(os.Stderr, "export: provide exactly one of --mbox, --eml, --maildir, --ics, or --vcf")
		os.Exit(1)
	}

	cfg, err := loadCLIConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Open the canonical stores for the configured backend (bbolt or postgres).
	st, err := openMailCLIStores(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "export: %v\n", err)
		os.Exit(1)
	}
	defer st.close()
	if account, gerr := st.account.GetAccount(domain, localPart); gerr != nil || account == nil {
		fmt.Fprintf(os.Stderr, "export: account %s does not exist\n", *user)
		os.Exit(1)
	}

	email := localPart + "@" + domain

	// Calendar (.ics) and contacts (.vcf) export reads the canonical collaboration
	// store, not the mail index — a separate path.
	if *icsPath != "" || *vcfPath != "" {
		cmdExportPIM(st, email, *icsPath, *vcfPath)
		return
	}

	folders := []string{*folder}
	if *folder == "" {
		folders, err = st.index.ListMailboxes(email)
		if err != nil {
			fmt.Fprintf(os.Stderr, "export: list mailboxes: %v\n", err)
			os.Exit(1)
		}
	}

	msgs := collectMessages(email, folders, st.index, st.blob)
	if len(msgs) == 0 {
		fmt.Println("export: no messages found")
		return
	}

	switch {
	case *mboxPath != "":
		err = exportMbox(*mboxPath, msgs)
	case *emlDir != "":
		err = exportEML(*emlDir, msgs)
	default:
		err = exportMaildir(*maildirDir, msgs)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "export: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("export: %d message(s) from %s written\n", len(msgs), email)
}

// cmdExportPIM writes the mailbox's calendar events + tasks (.ics) or contacts
// (.vcf) from the canonical collaboration store to one interchange file: a single
// VCALENDAR holding every VEVENT and VTODO (timezones deduplicated) or a vCard
// file holding every contact. It is the inverse of `umailserver import --ics|--vcf`.
func cmdExportPIM(st *mailCLIStores, email, icsPath, vcfPath string) {
	var out []byte
	var dst, label string
	var count int
	if icsPath != "" {
		dst, label = icsPath, "calendar object"
		events, err := st.cal.GetEvents(email, "default")
		if err != nil {
			fmt.Fprintf(os.Stderr, "export: read calendar: %v\n", err)
			os.Exit(1)
		}
		tasks, err := st.task.GetEvents(email, "default")
		if err != nil {
			fmt.Fprintf(os.Stderr, "export: read tasks: %v\n", err)
			os.Exit(1)
		}
		docs := append(append([]string{}, events...), tasks...)
		count = len(docs)
		out = pimport.MergeICS(docs)
	} else {
		dst, label = vcfPath, "contact"
		docs, err := st.card.GetContacts(email, "default")
		if err != nil {
			fmt.Fprintf(os.Stderr, "export: read contacts: %v\n", err)
			os.Exit(1)
		}
		count = len(docs)
		out = pimport.MergeVCF(docs)
	}
	if count == 0 {
		fmt.Printf("export: no %s objects found\n", label)
		return
	}
	if err := os.WriteFile(filepath.Clean(dst), out, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "export: write %s: %v\n", dst, err)
		os.Exit(1)
	}
	fmt.Printf("export: %d %s(s) from %s written\n", count, label, email)
}

// exportedMessage is one message read from the store with its source folder.
type exportedMessage struct {
	folder string
	raw    []byte
}

// collectMessages reads every message in the given folders from the canonical
// store, in (folder, UID) order.
func collectMessages(email string, folders []string, index imap.MetadataStore, msgStore *storage.MessageStore) []exportedMessage {
	var out []exportedMessage
	for _, folder := range folders {
		uids, err := index.GetMessageUIDs(email, folder)
		if err != nil {
			continue
		}
		for _, uid := range uids {
			meta, merr := index.GetMessageMetadata(email, folder, uid)
			if merr != nil {
				continue
			}
			raw, rerr := msgStore.ReadMessage(email, meta.MessageID)
			if rerr != nil {
				continue
			}
			out = append(out, exportedMessage{folder: folder, raw: raw})
		}
	}
	return out
}

// exportMbox writes all messages to one mbox file (flat — mbox has no folders).
func exportMbox(path string, msgs []exportedMessage) error {
	f, err := os.Create(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("create mbox: %w", err)
	}
	defer closeImportStore("mbox file", f)
	raws := make([][]byte, len(msgs))
	for i, m := range msgs {
		raws[i] = m.raw
	}
	return mailexport.WriteMbox(f, raws)
}

// exportEML writes one .eml per message under dir, grouped into folder subdirs.
func exportEML(dir string, msgs []exportedMessage) error {
	seq := map[string]int{}
	for _, m := range msgs {
		sub := filepath.Join(dir, filepath.FromSlash(m.folder))
		if err := os.MkdirAll(sub, 0o750); err != nil {
			return fmt.Errorf("create %q: %w", sub, err)
		}
		seq[m.folder]++
		name := filepath.Join(sub, fmt.Sprintf("%05d.eml", seq[m.folder]))
		if err := os.WriteFile(name, m.raw, 0o600); err != nil {
			return fmt.Errorf("write %q: %w", name, err)
		}
	}
	return nil
}

// exportMaildir writes a Maildir tree: INBOX at the root, other folders as
// Maildir++ "." subfolders, each message a cur/ file flagged Seen.
func exportMaildir(dir string, msgs []exportedMessage) error {
	seq := 0
	for _, m := range msgs {
		box := maildirBox(dir, m.folder)
		cur := filepath.Join(box, "cur")
		for _, sub := range []string{cur, filepath.Join(box, "new"), filepath.Join(box, "tmp")} {
			if err := os.MkdirAll(sub, 0o750); err != nil {
				return fmt.Errorf("create %q: %w", sub, err)
			}
		}
		seq++
		name := filepath.Join(cur, fmt.Sprintf("%d.export.umailserver:2,S", seq))
		if err := os.WriteFile(name, m.raw, 0o600); err != nil {
			return fmt.Errorf("write %q: %w", name, err)
		}
	}
	return nil
}

// maildirBox maps a mailbox name to its Maildir directory: INBOX (or empty) is
// the root; other folders become Maildir++ "." entries ("Work/Projects" ->
// ".Work.Projects"), the inverse of mailimport's maildirFolderName.
func maildirBox(base, folder string) string {
	if folder == "" || strings.EqualFold(folder, "INBOX") {
		return base
	}
	return filepath.Join(base, "."+strings.ReplaceAll(folder, "/", "."))
}

func cmdDB(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: umailserver db <subcommand>")
		fmt.Println("Subcommands: status, migrate, rollback")
		os.Exit(1)
	}

	subcmd := args[0]

	switch subcmd {
	case "status":
		cmdDBStatus(args[1:])
	case "migrate":
		cmdDBMigrate(args[1:])
	case "rollback":
		cmdDBRollback(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown db subcommand: %s\n", subcmd)
		fmt.Println("Usage: umailserver db <subcommand>")
		fmt.Println("Subcommands: status, migrate, rollback")
		os.Exit(1)
	}
}

func cmdDBStatus(args []string) {
	if len(args) == 0 {
		requireBboltBackend("db status")
	}
	dbPath := getDatabasePath()
	if len(args) > 0 {
		dbPath = filepath.Join(args[0], "umailserver.db")
	}
	database, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	registry := migrations.NewRegistry()
	migrations.InitMigrations(registry)
	migrator := migrations.NewMigrator(database.BoltDB(), registry)

	status, err := migrator.Status()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get migration status: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Database migrations:\n")
	fmt.Printf("  Applied: %d\n", status.Applied)
	fmt.Printf("  Pending: %d\n", status.Pending)
	fmt.Printf("  Total:   %d\n", status.Total)

	if status.Pending > 0 {
		fmt.Println("\nRun 'umailserver db migrate' to apply pending migrations.")
	}
}

func cmdDBMigrate(args []string) {
	if len(args) == 0 {
		requireBboltBackend("db migrate")
	}
	dbPath := getDatabasePath()
	if len(args) > 0 {
		dbPath = filepath.Join(args[0], "umailserver.db")
	}
	database, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	fmt.Println("Running database migrations...")

	registry := migrations.NewRegistry()
	migrations.InitMigrations(registry)
	migrator := migrations.NewMigrator(database.BoltDB(), registry)

	if err := migrator.Migrate(); err != nil {
		fmt.Fprintf(os.Stderr, "Migration failed: %v\n", err)
		os.Exit(1)
	}

	status, _ := migrator.Status()
	fmt.Printf("Migration complete. Applied: %d, Pending: %d\n", status.Applied, status.Pending)
}

func cmdDBRollback(args []string) {
	if len(args) == 0 {
		requireBboltBackend("db rollback")
	}
	dbPath := getDatabasePath()
	if len(args) > 0 {
		dbPath = filepath.Join(args[0], "umailserver.db")
	}
	database, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	fmt.Println("Rolling back last migration...")

	registry := migrations.NewRegistry()
	migrations.InitMigrations(registry)
	migrator := migrations.NewMigrator(database.BoltDB(), registry)

	if err := migrator.Rollback(); err != nil {
		fmt.Fprintf(os.Stderr, "Rollback failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Rollback complete.")
}

func cmdStatus(args []string) {
	dataDir := getDataDir()
	if len(args) > 0 {
		dataDir = args[0]
	}

	pidFile := server.NewPIDFile(dataDir)
	pid, err := pidFile.Read()
	if err != nil {
		fmt.Println("Status: not running")
		os.Exit(0)
	}

	fmt.Printf("Status: running\n")
	fmt.Printf("PID: %d\n", pid)

	// Try to get more info from health endpoint
	// This would require the admin API to be accessible
	// For now, just show basic info
}

func cmdStop(args []string) {
	dataDir := getDataDir()
	if len(args) > 0 {
		dataDir = args[0]
	}

	pidFile := server.NewPIDFile(dataDir)
	pid, err := pidFile.Read()
	if err != nil {
		fmt.Println("Server is not running")
		os.Exit(0)
	}

	fmt.Printf("Stopping server (PID: %d)...\n", pid)

	// Send SIGTERM
	proc, err := os.FindProcess(pid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to find process: %v\n", err)
		os.Exit(1)
	}

	if err := proc.Signal(os.Interrupt); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to signal process: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✓ Stop signal sent")
}

func cmdRestart(args []string) {
	dataDir := getDataDir()
	if len(args) > 0 {
		dataDir = args[0]
	}

	// Stop if running
	pidFile := server.NewPIDFile(dataDir)
	if pid, err := pidFile.Read(); err == nil && pid > 0 {
		fmt.Printf("Stopping server (PID: %d)...\n", pid)
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(os.Interrupt)
			// Wait a bit for shutdown
			time.Sleep(2 * time.Second)
		}
	}

	// Start again
	fmt.Println("Starting server...")
	cmdServe([]string{"--data-dir", dataDir})
}
