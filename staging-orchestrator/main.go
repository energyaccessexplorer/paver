package main

import (
	"bytes"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

type config struct {
	socket            string
	ticketsPath       string
	runDir            string
	pubkey            string
	buckets           string
	deployToken       string
	deployTokenFile   string
	idleTimeout       time.Duration
	startupTimeout    time.Duration
	maxInstances      int
	departerBuilds    string
	departerWorkspace string
	publicOrigin      string
}

var (
	logfile     *os.File
	logfilename string
	logger      *log.Logger
)

func main() {
	cfg := parse_flags()

	logger_setup()

	cfg.deployToken = read_deploy_token(cfg.deployTokenFile)

	if err := os.MkdirAll(cfg.runDir, 0755); err != nil {
		logger.Fatalf("could not create run dir %s: %v", cfg.runDir, err)
	}

	reg := &registry{
		m:   map[string]*instance{},
		cfg: cfg,
	}

	go reaper_loop(reg)

	mux := http.NewServeMux()
	mux.HandleFunc("/status", status_handler(reg))
	mux.HandleFunc("/deploy/", deploy_handler(reg, cfg.deployToken))
	mux.HandleFunc("/", proxy_handler(reg))

	os.Remove(cfg.socket)
	l, err := net.Listen("unix", cfg.socket)
	if err != nil {
		logger.Fatalf("could not listen on %s: %v", cfg.socket, err)
	}
	os.Chmod(cfg.socket, 0777)

	logger.Printf("orchestrator listening on %s (tickets_path=%s max_instances=%d idle_timeout=%s)",
		cfg.socket, cfg.ticketsPath, cfg.maxInstances, cfg.idleTimeout)
	println("Listening on socket:", cfg.socket)

	logger.Fatal(http.Serve(l, mux))
}

func parse_flags() *config {
	cfg := &config{}

	flag.StringVar(&cfg.socket, "socket", "/tmp/paver-staging-orchestrator.sock", "Socket file to run on")
	flag.StringVar(&cfg.ticketsPath, "tickets-path", "/var/www/paver-tickets", "Directory containing per-ticket built paver binaries (<tickets-path>/<TICKET>/paver)")
	flag.StringVar(&cfg.runDir, "run-dir", "/tmp/paver-staging-orchestrator", "Directory for per-ticket sockets/logs/tmpdirs")
	flag.StringVar(&cfg.pubkey, "pubkey", "", "Public key file to pass through to spawned paver instances")
	flag.StringVar(&cfg.buckets, "buckets", "/etc/paver-buckets.json", "S3 buckets config to pass through to spawned paver instances")
	flag.StringVar(&cfg.deployTokenFile, "deploy-token-file", "", "File containing the bearer token CI must present to POST /deploy/<TICKET> (required; the endpoint refuses all requests if unset). A file, not a flag value, so the token never appears in argv/ps or a world-readable systemd unit.")
	flag.StringVar(&logfilename, "log", "/tmp/paver-staging-orchestrator.log", "")
	flag.DurationVar(&cfg.idleTimeout, "idle-timeout", 30*time.Minute, "Kill a per-ticket instance after this much time with no requests")
	flag.DurationVar(&cfg.startupTimeout, "startup-timeout", 10*time.Second, "How long to wait for a newly-launched instance to become ready")
	flag.IntVar(&cfg.maxInstances, "max-instances", 5, "Maximum number of concurrently-running per-ticket instances (LRU-evicted beyond this)")
	flag.StringVar(&cfg.departerBuilds, "departer-builds-path", "/var/www/departer-tickets", "Directory for per-ticket departer build output (logs + zips; served at /<TICKET>/departer/builds/)")
	flag.StringVar(&cfg.departerWorkspace, "departer-workspace", "/var/cache/offroad", "Offroad workspace the per-ticket departer runs its build in")
	flag.StringVar(&cfg.publicOrigin, "public-origin", "https://paver.energyaccessexplorer.org", "External origin this orchestrator is reachable at; prepended to the download link ticket departers write into their build logs")

	flag.Parse()

	return cfg
}

func read_deploy_token(path string) string {
	if path == "" {
		logger.Println("warning: -deploy-token-file unset, /deploy/ will refuse all requests")
		return ""
	}

	b, err := os.ReadFile(path)
	if err != nil {
		logger.Fatalf("could not read -deploy-token-file %s: %v", path, err)
	}

	return strings.TrimSpace(string(b))
}

func logger_setup() {
	var err error

	logfile, err = os.OpenFile(logfilename, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		panic(err)
	}

	var logbuf bytes.Buffer
	logger = log.New(&logbuf, "", log.LstdFlags)
	logger.SetOutput(logfile)

	println("Logging to:", logfilename)
}
