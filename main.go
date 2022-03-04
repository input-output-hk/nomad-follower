package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/alexflint/go-arg"
	nomad "github.com/hashicorp/nomad/api"
	vault "github.com/hashicorp/vault/api"
	"github.com/pkg/errors"
)

var NOMAD_MAX_WAIT = 5 * time.Minute

type nomadFollower struct {
	logger         *log.Logger
	nomadClient    *nomad.Client
	vaultClient    *vault.Client
	queryOptions   *nomad.QueryOptions
	ctx            context.Context
	configM        *sync.Mutex
	allocs         *allocations
	configFile     string
	stateDir       string
	allocPrefix    string
	lokiUrl        string
	nomadNamespace string
	vectorStart    chan bool
	vectorStarted  bool
	vaultTokenFile string
}

type cli struct {
	State     string `arg:"--state" help:"dir for vector and index state"`
	Alloc     string `arg:"--alloc" help:"Prefix for Nomad allocation directories, %s will be replaced by the Allocation ID and globs are allowed"`
	LokiUrl   string `arg:"--loki-url" help:"Loki Base URL"`
	Namespace string `arg:"--namespace" help:"Nomad namespace to monitor"`
}

func (c *cli) Version() string { return buildVersion + " (" + buildCommit + ")" }

var buildVersion = "dev"
var buildCommit = "dirty"

func main() {
	logger := log.New(os.Stderr, "", log.LstdFlags)

	args := &cli{
		State:     "./state",
		Alloc:     "/tmp/NomadClient*/%s/alloc",
		LokiUrl:   "http://localhost:3100",
		Namespace: "default",
	}
	arg.MustParse(args)

	nomadConfig := nomad.DefaultConfig()
	nomadClient, err := nomad.NewClient(nomadConfig)
	die(logger, errors.WithMessage(err, "While creating Nomad client"))

	vaultClient, err := vault.NewClient(nil)
	die(logger, errors.WithMessage(err, "While creating Vault client"))

	f := &nomadFollower{
		logger:         logger,
		nomadClient:    nomadClient,
		vaultClient:    vaultClient,
		queryOptions:   &nomad.QueryOptions{Namespace: args.Namespace},
		configM:        &sync.Mutex{},
		configFile:     filepath.Join(args.State, "vector.toml"),
		stateDir:       args.State,
		allocPrefix:    args.Alloc,
		lokiUrl:        args.LokiUrl,
		nomadNamespace: args.Namespace,
		vectorStart:    make(chan bool),
		vectorStarted:  false,
		vaultTokenFile: os.Getenv("VAULT_TOKEN_FILE"),
	}

	err = os.MkdirAll(filepath.Join(f.stateDir, "vector"), 0o755)
	die(logger, errors.WithMessage(err, "While creating state dir"))

	err = f.start()
	die(logger, errors.WithMessage(err, "While starting"))
}

func (f *nomadFollower) getNomadClient() *nomad.Client {
	return f.nomadClient
}

func die(logger *log.Logger, err error) {
	if err != nil {
		logger.Println(err.Error())
		os.Exit(1)
	}
}
