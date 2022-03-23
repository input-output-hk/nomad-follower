package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	nomad "github.com/hashicorp/nomad/api"
	"github.com/pkg/errors"
)

type VectorConfig struct {
	DataDir    string                 `toml:"data_dir"`
	Timezone   string                 `toml:"timezone"`
	Sources    map[string]interface{} `toml:"sources"`
	Transforms map[string]interface{} `toml:"transforms"`
	Sinks      map[string]interface{} `toml:"sinks"`
}

type VectorSourceJournald struct {
	Type          string `toml:"type"`
	RemapPriority bool   `toml:"remap_priority"`
}

type VectorSourceFile struct {
	Type            string   `toml:"type"`
	IgnoreOlderSecs uint64   `toml:"ignore_older_secs"`
	Include         []string `toml:"include"`
	LineDelimiter   string   `toml:"line_delimiter"`
	ReadFrom        string   `toml:"read_from"`
}

type VectorTransformRemap struct {
	Type   string   `toml:"type"`
	Inputs []string `toml:"inputs"`
	Source string   `toml:"source"`
}

type VectorSinkConsole struct {
	Type     string                    `toml:"type"`
	Inputs   []string                  `toml:"inputs"`
	Target   string                    `toml:"target"`
	Encoding VectorSinkConsoleEncoding `toml:"encoding"`
}

type VectorSinkConsoleEncoding struct {
	Codec string `toml:"codec"`
}

type VectorSinkFile struct {
	Type        string                 `toml:"type"`
	Inputs      []string               `toml:"inputs"`
	Path        string                 `toml:"path"`
	Compression string                 `toml:"compression"`
	Encoding    VectorSinkFileEncoding `toml:"encoding"`
}

type VectorSinkFileEncoding struct {
	Codec string `toml:"codec"`
}

type VectorSinkLoki struct {
	Type     string                 `toml:"type"`
	Inputs   []string               `toml:"inputs"`
	Endpoint string                 `toml:"endpoint"`
	Labels   map[string]string      `toml:"labels"`
	Encoding VectorSinkLokiEncoding `toml:"encoding"`
}

type VectorSinkLokiEncoding struct {
	Codec           string   `toml:"codec"`
	TimestampFormat string   `toml:"timestamp_format"`
	OnlyFields      []string `toml:"only_fields"`
}

func (f *nomadFollower) start() error {
	f.setTokenFromEnvironment()
	go f.reloadToken()
	go f.checkToken()
	go f.listen()

	f.logger.Println("Waiting for valid Vector configuration")
	<-f.vectorStart
	f.logger.Println("Starting Vector")
	return f.vector()
}

func (f *nomadFollower) reloadToken() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP)

	for sig := range c {
		println(sig)
		fmt.Printf("Got A HUP Signal! Now Reloading Nomad Token....\n")
		f.setTokenFromEnvironment()
	}
}

func (f *nomadFollower) setTokenFromEnvironment() {
	if b, err := os.ReadFile(f.nomadTokenFile); err != nil {
		f.logger.Fatalf("Failed to read the nomad token file at %s", f.nomadTokenFile)
	} else {
		f.nomadClient.SetSecretID(strings.TrimSpace(string(b)))
		f.logger.Printf("Nomad Token reloaded\n")
	}
}

// Just checks that the current token is still fine regularly
func (f *nomadFollower) checkToken() {
	for {
		if currentToken, _, err := f.nomadClient.ACLTokens().Self(nil); err != nil {
			f.logger.Fatal(err)
		} else {
			f.logger.Printf("Current Token: id: %s name: %s policies: %v\n", currentToken.AccessorID, currentToken.Name, currentToken.Policies)
		}
		time.Sleep(1 * time.Minute)
	}
}

// Listen to Nomad events and update Vector config accordingly
func (f *nomadFollower) listen() error {
	self, err := f.nomadClient.Agent().Self()
	die(f.logger, errors.WithMessage(err, "While looking up the local agent"))

	nodeID := self.Stats["client"]["node_id"]
	f.populateAllocs(nodeID)
	topics := map[nomad.Topic][]string{nomad.TopicAllocation: {"*"}}
	index := f.loadIndex()
	eventStream := f.nomadClient.EventStream()
	events, err := eventStream.Stream(context.Background(), topics, index, f.queryOptions)
	die(f.logger, errors.WithMessage(err, "While starting the event stream"))

	f.writeConfig()

	for event := range events {
		if event.Err != nil {
			return err
		}

		if event.IsHeartbeat() {
			continue
		}

		f.saveIndex(event.Index)
		f.eventHandler(event.Events, nodeID)
	}

	return nil
}

func (f *nomadFollower) loadIndex() uint64 {
	bindex, err := os.ReadFile(filepath.Join(f.stateDir, "index"))
	if err != nil {
		return 0
	}
	index, err := strconv.ParseUint(string(bindex), 10, 64)
	if err != nil {
		return 0
	}
	return index
}

func (f *nomadFollower) saveIndex(index uint64) {
	sindex := strconv.FormatUint(index, 10)
	err := os.WriteFile(filepath.Join(f.stateDir, "index"), []byte(sindex), 0o644)
	if err != nil {
		f.logger.Printf("Couldn't write index: %s\n", err.Error())
	}
}

func (f *nomadFollower) populateAllocs(nodeID string) {
	f.allocs = &allocations{
		allocs: map[string]*nomad.Allocation{},
		lock:   &sync.RWMutex{},
	}

	allocs, _, err := f.nomadClient.Allocations().List(f.queryOptions)
	die(f.logger, errors.WithMessage(err, "While listing allocations"))

	for _, allocStub := range allocs {
		alloc, _, err := f.nomadClient.Allocations().Info(allocStub.ID, f.queryOptions)
		if err != nil {
			f.logger.Fatal(err)
		}
		if alloc.NodeID != nodeID {
			continue
		}
		switch alloc.ClientStatus {
		case "pending", "running":
			f.allocs.Add(alloc)
		}
	}
}

func (f *nomadFollower) generateVectorConfig() *VectorConfig {
	sources := map[string]interface{}{}
	transforms := map[string]interface{}{}
	sinks := map[string]interface{}{}

	f.allocs.Each(func(id string, alloc *nomad.Allocation) {
		prefix := fmt.Sprintf(f.allocPrefix, id)

		taskNames := []string{}
		for name := range alloc.TaskResources {
			taskNames = append(taskNames, name)
		}

		for _, taskName := range taskNames {
			f.logger.Printf("Adding job %s task %s", alloc.ID, taskName)
			for _, source := range []string{"stdout", "stderr"} {
				sourceName := "source_" + source + "_" + taskName + "_" + id
				sources[sourceName] = VectorSourceFile{
					Type:            "file",
					IgnoreOlderSecs: 300,
					Include:         []string{filepath.Join(prefix, "logs/"+taskName+"."+source+".[0-9]*")},
					LineDelimiter:   "\r\n",
					ReadFrom:        "beginning",
				}

				transforms["transform_"+source+"_"+taskName+"_"+id] = VectorTransformRemap{
					Inputs: []string{sourceName},
					Type:   "remap",
					Source: `
					.source = "` + source + `"
					.nomad_alloc_id = "` + alloc.ID + `"
					.nomad_job_id = "` + alloc.JobID + `"
					.nomad_alloc_name = "` + alloc.Name + `"
					.nomad_namespace = "` + alloc.Namespace + `"
					.nomad_node_id = "` + alloc.NodeID + `"
					.nomad_node_name = "` + alloc.NodeName + `"
					.nomad_task_group = "` + alloc.TaskGroup + `"
					.nomad_task_name = "` + taskName + `"
				`,
				}
			}
		}
	})

	sinks = map[string]interface{}{
		"loki": VectorSinkLoki{
			Type:     "loki",
			Inputs:   []string{"transform_stdout_*", "transform_stderr_*"},
			Endpoint: f.lokiUrl,
			Labels: map[string]string{
				"source":           "{{ source }}",
				"nomad_alloc_id":   "{{ nomad_alloc_id }}",
				"nomad_job_id":     "{{ nomad_job_id }}",
				"nomad_alloc_name": "{{ nomad_alloc_name }}",
				"nomad_namespace":  "{{ nomad_namespace }}",
				"nomad_node_id":    "{{ nomad_node_id }}",
				"nomad_node_name":  "{{ nomad_node_name }}",
				"nomad_task_group": "{{ nomad_task_group }}",
				"nomad_task_name":  "{{ nomad_task_name }}",
			},
			Encoding: VectorSinkLokiEncoding{
				Codec:           "text",
				TimestampFormat: "rfc3339",
				OnlyFields:      []string{"message"},
			},
		},
	}

	return &VectorConfig{
		DataDir:    filepath.Join(f.stateDir, "vector"),
		Timezone:   "UTC",
		Sources:    sources,
		Transforms: transforms,
		Sinks:      sinks,
	}
}

func (f *nomadFollower) vector() error {
	cmd := exec.Command(
		"vector",
		"--watch-config", f.configFile,
		"--config-toml", f.configFile,
		"--quiet",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (f *nomadFollower) eventHandler(events []nomad.Event, nodeID string) {
	for _, event := range events {
		if event.Topic == nomad.TopicAllocation {
			alloc, err := event.Allocation()
			if err == nil && alloc.NodeID == nodeID {
				f.eventHandleAllocation(alloc)
			}
		}
	}
}

func (f *nomadFollower) eventHandleAllocation(alloc *nomad.Allocation) {
	switch alloc.ClientStatus {
	case "pending", "running":
		f.allocs.Add(alloc)
		f.writeConfig()
	case "complete", "failed":
		go func() {
			// Give Vector time to scoop up all outstanding logs
			time.Sleep(30 * time.Second)
			f.allocs.Del(alloc)
			f.writeConfig()
		}()
	default:
		fmt.Println(alloc.NodeID, alloc.JobID, alloc.ID, alloc.ClientStatus)
	}
}

func (f *nomadFollower) writeConfig() error {
	f.configM.Lock()
	defer f.configM.Unlock()

	time.Sleep(1 * time.Second)

	f.logger.Println("Writing config to", f.configFile)

	buf := bytes.Buffer{}

	if err := toml.NewEncoder(&buf).Encode(f.generateVectorConfig()); err != nil {
		return errors.WithMessage(err, "While encoding the vector config")
	}

	if err := os.WriteFile(f.configFile+".new", buf.Bytes(), 0o777); err != nil {
		return errors.WithMessage(err, "while writing the new config file")
	}

	if err := f.validateNewConfig(); err != nil {
		return errors.WithMessage(err, "while validating vector config")
	} else {
		if !f.vectorStarted {
			f.vectorStarted = true
			f.vectorStart <- true
		}
	}

	if err := os.Rename(f.configFile+".new", f.configFile); err != nil {
		return errors.WithMessage(err, "while replacing the old config")
	}

	return nil
}

func (f *nomadFollower) validateNewConfig() error {
	cmd := exec.Command("vector", "validate", f.configFile+".new")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
