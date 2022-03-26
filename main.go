package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/alexflint/go-arg"
	nomad "github.com/hashicorp/nomad/api"
	"github.com/pkg/errors"
)

var NOMAD_MAX_WAIT = 5 * time.Minute

type nomadFollower struct {
	logger         *log.Logger
	nomadClient    *nomad.Client
	queryOptions   *nomad.QueryOptions
	ctx            context.Context
	allocs         *allocations
	configFile     string
	stateDir       string
	allocPrefix    string
	lokiUrl        string
	nomadNamespace string
	vectorStart    chan bool
	vectorStarted  bool
	configUpdated  chan bool
	tokenUpdated   chan string
	nomadTokenFile string
}

type cli struct {
	State     string `arg:"--state" help:"dir for vector and index state"`
	Alloc     string `arg:"--alloc" help:"Prefix for Nomad allocation directories, %s will be replaced by the Allocation ID and globs are allowed"`
	LokiUrl   string `arg:"--loki-url" help:"Loki Base URL"`
	Namespace string `arg:"--namespace" help:"Nomad namespace to monitor"`
	TokenFile string `arg:"--token-file" help:"Nomad token file"`
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
		Namespace: "*",
	}
	arg.MustParse(args)

	nomadConfig := nomad.DefaultConfig()
	nomadClient, err := nomad.NewClient(nomadConfig)
	die(logger, errors.WithMessage(err, "While creating Nomad client"))

	f := &nomadFollower{
		logger:         logger,
		nomadClient:    nomadClient,
		queryOptions:   &nomad.QueryOptions{Namespace: args.Namespace},
		configFile:     filepath.Join(args.State, "vector.toml"),
		stateDir:       args.State,
		allocPrefix:    args.Alloc,
		lokiUrl:        args.LokiUrl,
		nomadNamespace: args.Namespace,
		vectorStart:    make(chan bool),
		vectorStarted:  false,
		configUpdated:  make(chan bool, 1000),
		tokenUpdated:   make(chan string),
		nomadTokenFile: args.TokenFile,
	}

	err = os.MkdirAll(filepath.Join(f.stateDir, "vector"), 0o755)
	die(logger, errors.WithMessage(err, "While creating state dir"))

	err = f.start()
	die(logger, errors.WithMessage(err, "While starting"))
}

func die(logger *log.Logger, err error) {
	if err != nil {
		logger.Println(err.Error())
		os.Exit(1)
	}
}
