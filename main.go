package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/alexflint/go-arg"
	"github.com/hashicorp/nomad/api"
	"github.com/pkg/errors"
)

var NOMAD_MAX_WAIT = 5 * time.Minute

type nomadFollower struct {
	logger       *log.Logger
	client       *api.Client
	queryOptions *api.QueryOptions
	ctx          context.Context
	configM      *sync.Mutex
	allocs       *allocations
	configFile   string
	stateDir     string
	allocPrefix  string
	lokiUrl      string
}

type cli struct {
	State   string `arg:"--state" help:"dir for vector and index state"`
	Alloc   string `arg:"--alloc" help:"Prefix for Nomad allocation directories, %s will be replaced by the Allocation ID and globs are allowed"`
	LokiUrl string `arg:"--loki-url" help:"Loki Base URL"`
}

func (c *cli) Version() string { return buildVersion + " (" + buildCommit + ")" }

var buildVersion = "dev"
var buildCommit = "dirty"

func main() {
	logger := log.New(os.Stderr, "", log.LstdFlags)

	args := &cli{
		State:   "./state",
		Alloc:   "/tmp/NomadClient*/%s/alloc",
		LokiUrl: "http://localhost:3100",
	}
	arg.MustParse(args)

	nomadConfig := api.DefaultConfig()
	client, err := api.NewClient(nomadConfig)
	die(logger, errors.WithMessage(err, "While creating Nomad client"))

	ctx, cancel := context.WithCancel(context.Background())

	f := &nomadFollower{
		logger:       logger,
		client:       client,
		queryOptions: &api.QueryOptions{Namespace: "default"},
		ctx:          ctx,
		configM:      &sync.Mutex{},
		configFile:   filepath.Join(args.State, "vector.toml"),
		stateDir:     args.State,
		allocPrefix:  args.Alloc,
		lokiUrl:      args.LokiUrl,
	}

	err = os.MkdirAll(filepath.Join(f.stateDir, "vector"), 0755)
	die(logger, errors.WithMessage(err, "While creating state dir"))

	go func() {
		c := make(chan os.Signal)
		signal.Notify(c, syscall.SIGINT)
		signal.Notify(c, syscall.SIGKILL)
		s := <-c
		logger.Printf("Received Signal '%s', gracefully shutting down\n", s.String())
		cancel()
	}()

	err = f.eventListener()
	die(logger, errors.WithMessage(err, "While starting eventListener"))
}

func die(logger *log.Logger, err error) {
	if err != nil {
		logger.Println(err.Error())
		os.Exit(1)
	}
}
